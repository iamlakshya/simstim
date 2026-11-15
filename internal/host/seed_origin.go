package host

import (
	"net/url"
	"strings"
	"unicode"
)

// safeSeedOrigin removes credentials and URL decorations before an origin
// enters the trusted manifest or the guest. Source credentials stay at the
// source. This does not grant any network or push access.
func safeSeedOrigin(raw string) string {
	if strings.ContainsFunc(raw, unicode.IsControl) {
		return ""
	}
	if !strings.Contains(raw, "://") {
		return raw
	}
	value, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if value.Scheme == "ssh" && value.User != nil {
		value.User = url.User(value.User.Username())
	} else {
		value.User = nil
	}
	value.RawQuery = ""
	value.Fragment = ""
	return value.String()
}
