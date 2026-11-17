package listing

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
)

// LegacyKey returns the SHA-256 hex digest of "url:" + legacyCanonical(raw).
// It is used to match vacancies imported from the previous application.
// Returns "" when raw cannot be parsed or uses an unsupported scheme.
func LegacyKey(raw string) string {
	lc, err := legacyCanonical(raw)
	if err != nil {
		return ""
	}
	h := sha256.Sum256([]byte("url:" + lc))
	return hex.EncodeToString(h[:])
}

// legacyCanonical applies the legacy app's URL normalisation. It differs from
// Canonical in four ways:
//   - scheme and host case is preserved (only the leading "www." is removed)
//   - only ONE trailing slash is removed from the path (TrimSuffix, not TrimRight)
//   - empty query values are written as k= (via url.Values.Encode)
//   - the escaped path is used (u.String() after mutating the url.URL)
func legacyCanonical(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errBadScheme(u.Scheme)
	}

	// Keep original host case; strip a leading "www." only.
	u.Host = strings.TrimPrefix(u.Host, "www.")
	u.Fragment = ""

	// Filter tracking params; Encode writes k= for empty values and sorts keys.
	q := u.Query()
	for k := range q {
		if dropParam(k) {
			delete(q, k)
		}
	}
	u.RawQuery = q.Encode()

	// Remove exactly one trailing slash from the decoded path.
	if u.Path != "/" {
		u.Path = strings.TrimSuffix(u.Path, "/")
	}
	if u.Path == "" {
		u.Path = "/"
	}
	// Clear RawPath so url.URL.String() re-encodes from Path.
	u.RawPath = ""

	return u.String(), nil
}

// errBadScheme returns a sentinel error for unsupported schemes.
type errBadScheme string

func (e errBadScheme) Error() string {
	return "listing: legacy key: unsupported scheme " + string(e)
}
