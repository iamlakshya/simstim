package seedmeta

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Remote strips credentials and URL decorations from a trusted seed remote.
// Local paths and ambiguous scp forms supply no remote fact to a model.
func Remote(raw string) string {
	if len(raw) > 2048 || !utf8.ValidString(raw) || strings.ContainsFunc(raw, unicode.IsControl) {
		return ""
	}
	if strings.Contains(raw, "://") {
		remote, err := url.Parse(raw)
		if err != nil || remote.Hostname() == "" || remote.Opaque != "" {
			return ""
		}
		switch remote.Scheme {
		case "http", "https", "git", "ssh":
		default:
			return ""
		}
		remote.User, remote.RawQuery, remote.Fragment = nil, "", ""
		remote.ForceQuery = false
		return remote.String()
	}
	if at := strings.IndexByte(raw, '@'); at >= 0 {
		if at == 0 || strings.ContainsAny(raw[:at], ":/\\") {
			return ""
		}
		raw = raw[at+1:]
	}
	colon := strings.IndexByte(raw, ':')
	if colon <= 0 || colon == len(raw)-1 || strings.ContainsAny(raw[:colon], "/\\@?#") || strings.ContainsAny(raw[colon+1:], "@?#") {
		return ""
	}
	return raw
}
