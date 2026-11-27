package egress

import (
	"errors"
	"net/http"
	"strings"
)

// One canonical interpretation covers every field line and case alias. The
// proxy never forwards coding fields after decoding, including identity-only
// declarations. A stack of actual compression formats is not supported.
func httpContentEncoding(header http.Header) (string, error) {
	encoding := ""
	for name, values := range header {
		if !strings.EqualFold(name, "Content-Encoding") {
			continue
		}
		if len(values) == 0 {
			return "", errHTTPContentEncoding()
		}
		for _, value := range values {
			for _, part := range strings.Split(value, ",") {
				coding := strings.ToLower(strings.Trim(part, " \t"))
				if coding == "" {
					return "", errHTTPContentEncoding()
				}
				if coding == "identity" {
					continue
				}
				if encoding != "" || coding != "gzip" && coding != "deflate" {
					return "", errHTTPContentEncoding()
				}
				encoding = coding
			}
		}
	}
	return encoding, nil
}

func errHTTPContentEncoding() error {
	return errors.New("invalid or stacked content encoding. Try: send identity with at most one gzip or deflate coding")
}

func clearHTTPContentEncoding(header http.Header) {
	for name := range header {
		if strings.EqualFold(name, "Content-Encoding") {
			delete(header, name)
		}
	}
}
