package egress

import "strings"

// Late routing, authentication, framing and representation controls cannot
// change the metadata used before forwarding. RFC 9110 section 6.5.1 defines
// these categories. Both proxy directions use this one policy gate.
func validHTTPPolicyTrailerName(name string) bool {
	if name == "" {
		return false
	}
	const separators = "()<>@,;:\\\"/[]?={}"
	for i := range len(name) {
		if name[i] <= ' ' || name[i] >= 0x7f || strings.ContainsRune(separators, rune(name[i])) {
			return false
		}
	}
	name = strings.ToLower(name)
	if strings.HasPrefix(name, "proxy-") || strings.HasPrefix(name, "if-") || strings.HasPrefix(name, "x-forwarded-") {
		return false
	}
	switch name {
	case "authorization", "cookie", "set-cookie", "forwarded", "cache-control", "connection", "content-encoding",
		"content-length", "content-range", "content-type", "expect", "host",
		"keep-alive", "max-forwards", "pragma", "range", "realm", "te",
		"trailer", "transfer-encoding", "upgrade", "via", "warning", "www-authenticate":
		return false
	default:
		return true
	}
}
