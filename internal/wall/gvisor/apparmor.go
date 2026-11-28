package gvisor

import (
	"fmt"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
)

// AppArmorProfileName returns the AppArmor profile file name for pinned runsc
// on Ubuntu hosts that restrict unprivileged user namespaces.
func AppArmorProfileName() string {
	return brand.Name + "-runsc"
}

// AppArmorProfilePath returns the installed AppArmor profile path under
// /etc/apparmor.d/.
func AppArmorProfilePath() string {
	return "/etc/apparmor.d/" + AppArmorProfileName()
}

// AppArmorProfile returns the minimal Ubuntu AppArmor 4.0 profile allowing the
// pinned runsc binary at runscPath to create user namespaces.
//
// Ubuntu's bwrap-userns-restrict profile cannot launch runsc because its child
// profile (unpriv_bwrap) enforces "audit deny capability,", which blocks runsc's
// gofer and Sentry namespace initialisation (CAP_SYS_ADMIN, CAP_SYS_CHROOT,
// CAP_SETPCAP and CAP_SYS_PTRACE inside the new user namespace). A dedicated
// profile scoped to the SHA-256-verified static runsc binary grants only userns
// creation to that binary.
func AppArmorProfile(runscPath string) string {
	return fmt.Sprintf(
		"abi <abi/4.0>,\ninclude <tunables/global>\n\nprofile %s %s flags=(unconfined) {\n  userns,\n}\n",
		AppArmorProfileName(), appArmorAttachPath(runscPath),
	)
}

// AppArmorFixCmd returns a single sudo command that installs and loads the
// AppArmor profile for runscPath on Ubuntu 24.04+. The helper never runs sudo
// itself; it prints this command in its Try: hint.
//
// The profile text is passed as a single-quoted positional argument to
// `printf '%s'` rather than interpolated into a double-quoted format string, so
// shell metacharacters or format specifiers in runscPath cannot inject commands.
func AppArmorFixCmd(runscPath string) string {
	path := AppArmorProfilePath()
	profile := AppArmorProfile(runscPath)
	return fmt.Sprintf(
		`sudo sh -c 'printf "%%s" "$1" > "$2" && chmod 0644 "$2" && apparmor_parser -r "$2"' _ %s %s`,
		shellSingleQuote(profile), shellSingleQuote(path),
	)
}

func appArmorAttachPath(runscPath string) string {
	clean := strings.Map(func(r rune) rune {
		if r == 0 || r == '\r' || r == '\n' {
			return -1
		}
		return r
	}, runscPath)
	needsQuote := clean == ""
	for i := range len(clean) {
		if !isPlainAppArmorPathByte(clean[i]) {
			needsQuote = true
			break
		}
	}
	if !needsQuote {
		return clean
	}
	escaped := strings.ReplaceAll(clean, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

func isPlainAppArmorPathByte(c byte) bool {
	return (c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') ||
		c == '/' || c == '_' || c == '.' || c == ':' || c == '+' || c == '-'
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func profileMatchesRunsc(profileText, runscPath string) bool {
	if profileText == "" || runscPath == "" {
		return false
	}
	header := "profile " + AppArmorProfileName() + " " + appArmorAttachPath(runscPath) + " "
	return strings.Contains(profileText, header)
}
