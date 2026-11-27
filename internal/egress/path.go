package egress

import (
	"fmt"
	"strings"

	"github.com/iamlakshya/simstim/internal/nettarget"
)

// canonicalPath decodes one path encoding layer and refuses ambiguous routes.
// Its result can pass through this function again without changing.
func canonicalPath(raw string) (string, error) {
	path, err := nettarget.CanonicalPath(raw)
	if err != nil {
		return "", fmt.Errorf("canonicalise network path: %w", err)
	}
	return path, nil
}

// coversRulePath compares the sets of visible paths named by two rules.
// An opaque flow is handled separately by matchesRulePath.
func coversRulePath(deny, allow compiledRule) bool {
	if deny.path == "" {
		return true
	}
	if allow.path == "" {
		return false
	}
	if deny.pathPrefix {
		return strings.HasPrefix(allow.path, deny.path)
	}
	return !allow.pathPrefix && allow.path == deny.path
}
