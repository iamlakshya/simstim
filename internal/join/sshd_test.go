package join

import (
	"strings"
	"testing"
)

func TestSSHDPolicyPreservesOnlyLocalForwards(t *testing.T) {
	text, err := SSHDDropIn("worker")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"Match User worker", "allowtcpforwarding local", "allowstreamlocalforwarding local", "permitlisten none", "allowagentforwarding no", "x11forwarding no", "permituserrc no"} {
		if !strings.Contains(text, line) {
			t.Fatalf("missing %q", line)
		}
	}
	if _, err = SSHDDropIn("worker\nMatch all"); err == nil {
		t.Fatal("accepted injected account")
	}
}
func TestEffectiveSSHDChecksEveryValue(t *testing.T) {
	var lines []string
	for _, v := range sshdValues {
		lines = append(lines, v[0]+" "+v[1])
	}
	good := strings.Join(lines, "\n")
	if err := VerifySSHD(good); err != nil {
		t.Fatal(err)
	}
	for n := range lines {
		changed := append([]string(nil), lines...)
		changed[n] = strings.Fields(changed[n])[0] + " yes"
		if err := VerifySSHD(strings.Join(changed, "\n")); err == nil {
			t.Fatalf("accepted wrong effective %s", lines[n])
		}
		missing := append(append([]string(nil), lines[:n]...), lines[n+1:]...)
		if err := VerifySSHD(strings.Join(missing, "\n")); err == nil {
			t.Fatalf("accepted missing effective %s", lines[n])
		}
	}
}
func TestSSHDIncludeMustBeActiveAndGlobal(t *testing.T) {
	for _, text := range []string{"Include /etc/ssh/sshd_config.d/*.conf\n", "Include sshd_config.d/*.conf\n", "Match all\nInclude /etc/ssh/sshd_config.d/*.conf"} {
		if !HasSSHDInclude(text) {
			t.Fatalf("missed %q", text)
		}
	}
	for _, text := range []string{"# Include /etc/ssh/sshd_config.d/*.conf", "Match User worker\nInclude /etc/ssh/sshd_config.d/*.conf", "Include /tmp/sshd/*.conf"} {
		if HasSSHDInclude(text) {
			t.Fatalf("accepted %q", text)
		}
	}
}
func TestLedgerRejectsUnknownAndRepeatedActions(t *testing.T) {
	l := &ledger{Version: 1, Account: "worker", Nonce: 1, KeyHash: strings.Repeat("a", 64), Entries: []ledgerEntry{{ID: "key", Kind: "file", State: "prepared"}}}
	if !validLedger(l) {
		t.Fatal("valid ahead intent refused")
	}
	l.Entries = append(l.Entries, l.Entries[0])
	if validLedger(l) {
		t.Fatal("duplicate ahead intent accepted")
	}
	l.Entries = l.Entries[:1]
	l.Entries[0].Kind = "shell"
	if validLedger(l) {
		t.Fatal("arbitrary undo accepted")
	}
}
