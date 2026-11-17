package listing

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Canonical returns the canonical address of a vacancy URL.
//
// Rules applied:
//   - Only http and https are accepted; other schemes return an error.
//   - Scheme and host are lowercased.
//   - A leading "www." on the host is removed.
//   - The fragment is removed.
//   - Query parameters whose key (case-insensitive) is utm_*, ref, source
//     or gh_src are removed; the remaining parameters are sorted by key.
//   - A trailing "/" on the path is removed; an empty path becomes "/".
func Canonical(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("listing: canonical: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("listing: canonical: unsupported scheme %q", u.Scheme)
	}
	host := strings.ToLower(u.Host)
	host = strings.TrimPrefix(host, "www.")

	// Filter query parameters; url.Values.Encode sorts keys and writes k= for
	// empty values, which is what we want.
	q := u.Query()
	for k := range q {
		if dropParam(k) {
			delete(q, k)
		}
	}

	// Rebuild with EscapedPath so percent-encoding in the path is preserved.
	u.Scheme = scheme
	u.Host = host
	u.Fragment = ""
	u.RawQuery = q.Encode()

	// Remove a single trailing slash from the escaped path; empty path → "/".
	ep := u.EscapedPath()
	if ep == "" {
		ep = "/"
	} else if ep != "/" {
		ep = strings.TrimSuffix(ep, "/")
	}
	u.RawPath = ep

	// url.URL.String uses RawPath when it differs from the encoding of Path.
	// We set RawPath directly, so we build the string manually to be safe.
	out := scheme + "://" + host + ep
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out, nil
}

var utmRE = regexp.MustCompile(`(?i)^utm_`)

// dropParam reports whether a query key should be dropped from a canonical URL.
func dropParam(key string) bool {
	lower := strings.ToLower(key)
	return utmRE.MatchString(lower) || lower == "ref" || lower == "source" || lower == "gh_src"
}

// ConcreteATS returns the canonical address when raw is the address of one
// concrete vacancy on Greenhouse, Lever, Ashby or Workable.
//
// Accepted forms (optional trailing "/apply" is stripped before canonicalising):
//   - boards.greenhouse.io/<board>/jobs/<id>
//   - job-boards.greenhouse.io/<board>/jobs/<id>
//   - jobs.lever.co/<site>/<uuid>
//   - jobs.ashbyhq.com/<name>/<uuid>
//   - apply.workable.com/<name>/j/<id>
//
// Board roots and unrecognised hosts return ("", false).
func ConcreteATS(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	host := strings.ToLower(u.Host)

	// Strip optional trailing "/apply".
	path := strings.TrimSuffix(u.Path, "/apply")
	// Also strip a trailing "/" before the apply check.
	path = strings.TrimRight(path, "/")

	var matched bool
	switch host {
	case "boards.greenhouse.io", "job-boards.greenhouse.io":
		// /<board>/jobs/<id>  — exactly three segments
		matched = matchPath(path, 3, 1, "jobs")
	case "jobs.lever.co":
		// /<site>/<uuid>  — exactly two segments
		matched = matchPath(path, 2, -1, "")
	case "jobs.ashbyhq.com":
		// /<name>/<uuid>  — exactly two segments
		matched = matchPath(path, 2, -1, "")
	case "apply.workable.com":
		// /<name>/j/<id>  — exactly three segments, middle is "j"
		matched = matchPath(path, 3, 1, "j")
	}
	if !matched {
		return "", false
	}

	// Rebuild with normalised path (the stripped one).
	rebuilt := scheme + "://" + host + path
	canon, err := Canonical(rebuilt)
	if err != nil {
		return "", false
	}
	return canon, true
}

// matchPath checks that path has exactly n segments (split by "/", ignoring
// the leading empty string), and when midIdx >= 0, checks that segment midIdx
// (0-based) equals midVal.
func matchPath(path string, n, midIdx int, midVal string) bool {
	segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(segs) != n {
		return false
	}
	for _, s := range segs {
		if s == "" {
			return false
		}
	}
	if midIdx >= 0 && segs[midIdx] != midVal {
		return false
	}
	return true
}
