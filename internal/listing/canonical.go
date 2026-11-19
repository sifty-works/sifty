package listing

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

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

	q := u.Query()
	for k := range q {
		if dropParam(k) {
			delete(q, k)
		}
	}

	u.Scheme = scheme
	u.Host = host
	u.Fragment = ""
	u.RawQuery = q.Encode()

	ep := u.EscapedPath()
	if ep == "" {
		ep = "/"
	} else if ep != "/" {
		ep = strings.TrimSuffix(ep, "/")
	}
	u.RawPath = ep

	out := scheme + "://" + host + ep
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out, nil
}

var utmRE = regexp.MustCompile(`(?i)^utm_`)

func dropParam(key string) bool {
	lower := strings.ToLower(key)
	return utmRE.MatchString(lower) || lower == "ref" || lower == "source" || lower == "gh_src"
}

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

	path := strings.TrimSuffix(strings.TrimSuffix(u.Path, "/application"), "/apply")
	path = strings.TrimRight(path, "/")

	var matched bool
	switch host {
	case "boards.greenhouse.io", "job-boards.greenhouse.io":
		matched = matchPath(path, 3, 1, "jobs")
	case "jobs.lever.co":
		matched = matchPath(path, 2, -1, "")
	case "jobs.ashbyhq.com":
		matched = matchPath(path, 2, -1, "")
	case "apply.workable.com":
		matched = matchPath(path, 3, 1, "j")
	}
	if !matched {
		return "", false
	}

	rebuilt := scheme + "://" + host + path
	canon, err := Canonical(rebuilt)
	if err != nil {
		return "", false
	}
	return canon, true
}

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
