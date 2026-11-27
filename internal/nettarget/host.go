package nettarget

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// NormaliseHost validates and canonicalises a DNS hostname or IP literal for a
// flow. It rejects empty labels, trailing multi-dots, zones, ports, paths,
// non-ASCII characters and all-numeric final labels.
func NormaliseHost(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("empty host")
	}
	for i := range len(raw) {
		c := raw[i]
		if c <= 0x20 || c >= 0x7f || c == '%' || c == '/' || c == '\\' || c == '@' {
			return "", fmt.Errorf("invalid character in host %q", raw)
		}
	}
	if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		inner := raw[1 : len(raw)-1]
		addr, err := netip.ParseAddr(inner)
		if err != nil || !addr.Is6() || addr.Zone() != "" {
			return "", fmt.Errorf("invalid bracketed IPv6 host %q", raw)
		}
		return addr.String(), nil
	}
	if addr, err := netip.ParseAddr(raw); err == nil && addr.Zone() == "" {
		return addr.String(), nil
	}
	if strings.Contains(raw, ":") {
		return "", fmt.Errorf("unexpected colon in host %q", raw)
	}
	s := strings.ToLower(strings.TrimSuffix(raw, "."))
	if s == "" || len(s) > 253 || strings.HasSuffix(s, ".") {
		return "", fmt.Errorf("invalid hostname %q", raw)
	}
	labels := strings.Split(s, ".")
	for _, l := range labels {
		if !validLabel(l) {
			return "", fmt.Errorf("invalid label %q in host %q", l, raw)
		}
	}
	if allDigits(labels[len(labels)-1]) {
		return "", fmt.Errorf("numeric final label in host %q", raw)
	}
	return s, nil
}

func validLabel(l string) bool {
	if len(l) == 0 || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
		return false
	}
	for i := range len(l) {
		c := l[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

func allDigits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}
