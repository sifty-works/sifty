package listing

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
)

func LegacyKey(raw string) string {
	lc, err := legacyCanonical(raw)
	if err != nil {
		return ""
	}
	h := sha256.Sum256([]byte("url:" + lc))
	return hex.EncodeToString(h[:])
}

func legacyCanonical(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errBadScheme(u.Scheme)
	}

	u.Host = strings.TrimPrefix(u.Host, "www.")
	u.Fragment = ""

	q := u.Query()
	for k := range q {
		if dropParam(k) {
			delete(q, k)
		}
	}
	u.RawQuery = q.Encode()

	if u.Path != "/" {
		u.Path = strings.TrimSuffix(u.Path, "/")
	}
	if u.Path == "" {
		u.Path = "/"
	}
	u.RawPath = ""

	return u.String(), nil
}

type errBadScheme string

func (e errBadScheme) Error() string {
	return "listing: legacy key: unsupported scheme " + string(e)
}
