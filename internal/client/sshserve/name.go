package sshserve

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
)

var targetRE = regexp.MustCompile(`^[a-z0-9-]+\.[a-z0-9-]+\.` + regexp.QuoteMeta(brand.Name) + `$`)

// ParseTarget validates and splits a session SSH hostname of the form
// <session>.<host>.<name> (spec 17.7). Only lower-case ASCII letters, digits
// and hyphens are accepted in each label so shell metacharacters never pass.
func ParseTarget(hostname string) (session, host string, err error) {
	if !targetRE.MatchString(hostname) {
		return "", "", fmt.Errorf("invalid session SSH name %q (want <session>.<host>.%s with lower-case letters, digits and hyphens)", hostname, brand.Name)
	}
	parts := strings.Split(hostname, ".")
	return parts[0], parts[1], nil
}
