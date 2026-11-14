package nettarget

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

func isNumericAddr(s string) bool {
	_, err := netip.ParseAddr(s)
	return err == nil
}

// SplitPath separates a rule target path from its host and optional port.
func SplitPath(s string) (hostPort, path string, err error) {
	idx := strings.IndexByte(s, '/')
	if idx < 0 {
		return s, "", nil
	}
	left, right := s[:idx], s[idx+1:]
	if strings.HasPrefix(left, "[") && strings.Contains(right, "]") {
		return s, "", nil
	}
	cidrPart := right
	if hostPart, _, hasColon := strings.Cut(right, ":"); hasColon && allDigits(hostPart) {
		cidrPart = hostPart
	}
	if allDigits(cidrPart) && looksLikeCIDRBase(left) {
		return s, "", nil
	}
	p := "/" + right
	if strings.ContainsAny(p, "?#\x00 \t\r\n") {
		return "", "", errors.New("rule path cannot contain query, fragment or control characters")
	}
	return left, p, nil
}

func looksLikeCIDRBase(left string) bool {
	if strings.Contains(left, "]") {
		return false
	}
	left = strings.TrimPrefix(left, "[")
	if strings.Count(left, ":") >= 2 {
		return true
	}
	for i := range len(left) {
		if (left[i] < '0' || left[i] > '9') && left[i] != '.' {
			return false
		}
	}
	return len(left) > 0
}

// SplitPort separates a numeric port from a hostname, address or CIDR.
func SplitPort(s string) (base string, port int, err error) {
	if strings.HasPrefix(s, "[") {
		end := strings.IndexByte(s, ']')
		if end < 0 {
			return "", 0, errors.New("missing closing ']'")
		}
		inner, rest := s[1:end], s[end+1:]
		if rest == "" {
			return inner, 0, nil
		}
		portStr, ok := strings.CutPrefix(rest, ":")
		if !ok {
			return "", 0, fmt.Errorf("unexpected %q after ']'", rest)
		}
		p, err := parsePort(portStr)
		return inner, p, err
	}
	if strings.Count(s, ":") >= 2 {
		return s, 0, nil
	}
	if host, portStr, ok := strings.Cut(s, ":"); ok {
		p, err := parsePort(portStr)
		return host, p, err
	}
	return s, 0, nil
}

func parsePort(s string) (int, error) {
	p, err := strconv.Atoi(s)
	if err != nil || p < 1 || p > 65535 || strconv.Itoa(p) != s {
		return 0, fmt.Errorf("invalid port %q", s)
	}
	return p, nil
}
