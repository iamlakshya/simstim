package join

import (
	"fmt"
	"strings"

	"github.com/iamlakshya/simstim/internal/join/code"
)

var sshdValues = [][2]string{
	{"authenticationmethods", "publickey"}, {"allowtcpforwarding", "local"},
	{"allowstreamlocalforwarding", "local"}, {"permitlisten", "none"},
	{"permittunnel", "no"}, {"allowagentforwarding", "no"},
	{"x11forwarding", "no"}, {"permituserrc", "no"},
}

// SSHDDropIn is the fixed account-scoped policy. Local Unix-socket forwards
// remain available; remote TCP and Unix-socket forwards are refused.
func SSHDDropIn(account string) (string, error) {
	if !code.ValidAccount(account) {
		return "", codeFailure("join_account", "the sshd drop-in needs a checked non-root account")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Match User %s\n", account)
	for _, v := range sshdValues {
		fmt.Fprintf(&b, "    %s %s\n", v[0], v[1])
	}
	b.WriteString("Match all\n")
	return b.String(), nil
}

// VerifySSHD checks sshd's effective per-account values, including defaults
// and settings in earlier Includes, rather than trusting the written file.
func VerifySSHD(output string) error {
	found := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 {
			k := strings.ToLower(f[0])
			if _, ok := found[k]; ok {
				return codeFailure("join_sshd", "sshd reported a duplicate effective value")
			}
			found[k] = strings.Join(f[1:], " ")
		}
	}
	for _, v := range sshdValues {
		if found[v[0]] != v[1] {
			return &code.Error{Code: "join_sshd", Message: "sshd's effective " + v[0] + " is not " + v[1], Hint: "Check earlier sshd settings, then retry host join. The old drop-in has been restored."}
		}
	}
	return nil
}

// HasSSHDInclude checks an active, global Include for the standard drop-in
// folder. A commented line or an Include within Match cannot authorise it.
func HasSSHDInclude(text string) bool {
	inMatch := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if strings.EqualFold(f[0], "Match") {
			inMatch = !(len(f) == 2 && strings.EqualFold(f[1], "all"))
			continue
		}
		if inMatch || !strings.EqualFold(f[0], "Include") {
			continue
		}
		for _, p := range f[1:] {
			p = strings.Trim(p, `"`)
			if p == "/etc/ssh/sshd_config.d/*.conf" || p == "sshd_config.d/*.conf" {
				return true
			}
		}
	}
	return false
}
func codeFailure(id, msg string) error {
	return &code.Error{Code: id, Message: msg, Hint: "Keep the root-owned leave script and ask the machine's owner to check the recorded change."}
}
