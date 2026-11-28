package api

import (
	"errors"
	"fmt"
	"strings"
)

// SecretBind describes how one bound secret attaches to outbound HTTP requests
// on the trusted side (isolation spec 6.1, 7.3 and 8.6). It carries only
// routing and header metadata, never the secret's value.
type SecretBind struct {
	Name   string   `json:"name"`
	Hosts  []string `json:"hosts"`
	Header string   `json:"header,omitempty"`
	Format string   `json:"format,omitempty"`
	Route  string   `json:"route,omitempty"`
}

// NormalizeSecretHost validates and normalises one destination hostname in a
// [secrets.NAME] hosts list. It rejects schemes, paths, wildcards, ports,
// whitespace and control characters.
func NormalizeSecretHost(raw string) (string, error) {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	if h == "" {
		return "", errors.New("secret host cannot be empty. Try: use a hostname such as api.anthropic.com")
	}
	if strings.Contains(h, "://") || strings.ContainsAny(h, "/?#@* \t\r\n:") {
		return "", fmt.Errorf("invalid secret host %q. Try: use a plain hostname without scheme, port or wildcard, such as api.anthropic.com", raw)
	}
	if len(h) > 253 {
		return "", fmt.Errorf("secret host %q is too long. Try: use a valid hostname under 253 characters", raw)
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", fmt.Errorf("invalid secret host %q. Try: use a valid hostname such as api.anthropic.com", raw)
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return "", fmt.Errorf("invalid secret host %q. Try: use an ASCII hostname such as api.anthropic.com", raw)
			}
		}
	}
	return h, nil
}

// NormalizeGatewayRoute validates an optional explicit gateway route slug
// (single lowercase path segment, e.g. "anthropic" or "acme-api").
func NormalizeGatewayRoute(raw string) (string, error) {
	s := strings.Trim(strings.ToLower(strings.TrimSpace(raw)), "/")
	if s == "" {
		return "", nil
	}
	if len(s) > 63 {
		return "", fmt.Errorf("gateway route %q is too long. Try: use a short route slug such as acme-api", raw)
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return "", fmt.Errorf("invalid gateway route %q. Try: use letters, digits, hyphens or underscores", raw)
		}
	}
	return s, nil
}

// RouteSlugForHost derives a deterministic gateway route slug for a normalised
// host (for example "api.example.com" -> "api-example-com").
func RouteSlugForHost(host string) string {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	var sb strings.Builder
	for _, r := range h {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			sb.WriteRune(r)
		default:
			sb.WriteByte('-')
		}
	}
	return strings.Trim(sb.String(), "-")
}

// ValidHeaderName reports whether raw is a syntactically valid HTTP header
// field name (RFC 9110 tchar subset).
func ValidHeaderName(raw string) bool {
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}
