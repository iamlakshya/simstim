package nettarget

import (
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"
)

// CanonicalPath decodes one path encoding layer and refuses ambiguous routes.
// Its result can pass through this function again without changing.
func CanonicalPath(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if raw[0] != '/' {
		return "", errors.New("a URL path must start with a slash")
	}
	decoded, err := url.PathUnescape(raw)
	if err != nil || !utf8.ValidString(decoded) {
		return "", errors.New("invalid URL path encoding")
	}
	if strings.ContainsAny(decoded, `\%?#`) || strings.Contains(decoded, "//") {
		return "", errors.New("ambiguous URL path")
	}
	for i := range len(decoded) {
		if decoded[i] < 0x20 || decoded[i] == 0x7f {
			return "", errors.New("a URL path cannot contain control characters")
		}
	}
	for _, segment := range strings.Split(decoded, "/") {
		if segment == "." || segment == ".." {
			return "", errors.New("a URL path cannot contain dot segments")
		}
	}
	return decoded, nil
}
