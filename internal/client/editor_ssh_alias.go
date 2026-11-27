package client

import (
	"regexp"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
)

var editorSSHNameV1 = regexp.MustCompile(`^[a-z0-9-]+$`)

// ParseEditorSSHAlias checks the whole alias before lookup or transport start.
// The original %n is used literally; no shell or fuzzy-host fallback exists.
func ParseEditorSSHAlias(value string) (session, host string, err error) {
	parts := strings.Split(value, ".")
	if len(value) > 512 || len(parts) != 3 || parts[2] != brand.Name || !editorSSHNameV1.MatchString(parts[0]) || !editorSSHNameV1.MatchString(parts[1]) || !ValidHostName(parts[1]) {
		return "", "", &UserError{Msg: "the SSH name is not an exact session and saved host alias", Hint: "Use <session>.<host>." + brand.Name + " from the generated SSH configuration.", Code: "bad_ssh_alias", Kind: KindUsage}
	}
	return parts[0], parts[1], nil
}
