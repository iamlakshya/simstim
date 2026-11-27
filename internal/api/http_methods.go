package api

import (
	"errors"
	"slices"
)

// FeatureHTTPMethods must be negotiated before sending any nonnil Methods,
// including an empty list. A legacy reader would otherwise broaden the rule.
const FeatureHTTPMethods = "policy-http-methods-v1"

// CanonicalHTTPMethods returns an owned exact-case set, preserving presence.
// JSON null and absence both decode to nil and retain legacy semantics.
func CanonicalHTTPMethods(raw *[]string) (*[]string, error) {
	if raw == nil {
		return nil, nil
	}
	methods := append([]string{}, (*raw)...)
	for _, method := range methods {
		if !ValidHTTPMethod(method) {
			return nil, errors.New("HTTP method must be a nonempty token without spaces or controls")
		}
	}
	slices.Sort(methods)
	methods = slices.Compact(methods)
	return &methods, nil
}

// ValidHTTPMethod follows the token grammar of RFC 9110 section 5.6.2.
// Method names are case-sensitive and must never be uppercased implicitly.
func ValidHTTPMethod(method string) bool {
	return ValidHTTPToken(method)
}

// ValidHTTPToken is the shared RFC token grammar for method and field names.
func ValidHTTPToken(token string) bool {
	if token == "" {
		return false
	}
	for i := range len(token) {
		if !ValidHTTPTokenByte(token[i]) {
			return false
		}
	}
	return true
}

// ValidHTTPTokenByte is the RFC 9110 token byte grammar shared by materialised
// methods/field names and incremental binding-header validation.
func ValidHTTPTokenByte(c byte) bool {
	if c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' {
		return true
	}
	switch c {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	default:
		return false
	}
}
