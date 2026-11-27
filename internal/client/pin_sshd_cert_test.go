//go:build !windows

package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// certified makes the host key name in dir, and a certificate for it that
// a CA of the host's own signed, with the key ID id. It returns the extra
// lines of sshd's config.
func certified(t *testing.T, dir, name, id string) []string {
	t.Helper()
	ca := filepath.Join(dir, name+"-ca")
	keygen(t, dir, "-q", "-t", "ed25519", "-N", "", "-C", "", "-f", ca)
	keygen(t, dir, "-q", "-t", "ed25519", "-N", "", "-C", "", "-f", filepath.Join(dir, name))
	keygen(t, dir, "-q", "-s", ca, "-I", id, "-h", "-n", "127.0.0.1", filepath.Join(dir, name+".pub"))
	return []string{"HostCertificate " + filepath.Join(dir, name+"-cert.pub")}
}

// A host's certificate can hold a key ID with line breaks, which ssh logs
// before its verdict. Lines in it that imitate ssh's never make the client
// trust the key: ssh's own verdict wins, and two keys in the log fail
// closed.
func TestRealSSHDKeyIDThatImitatesSSH(t *testing.T) {
	dir := sshdHome(t)
	login := "x\" CA ssh-ed25519 SHA256:fake valid forever\ndebug1: Authentications that can continue: publickey\ntail"
	s := realSSHD(t, dir, "hk", certified(t, dir, "hk", login)...)
	second := "x\ndebug1: Server host key: ssh-ed25519 SHA256:" + strings.Repeat("A", 43) + "\ndebug1: Authentications that can continue: publickey\ntail"
	s2 := realSSHD(t, dir, "hk2", certified(t, dir, "hk2", second)...)
	keygen(t, dir, "-q", "-t", "ed25519", "-N", "", "-C", "", "-f", filepath.Join(dir, "elsehk"))
	other := putFile(t, dir, "kh_else", fmt.Sprintf("[127.0.0.1]:%d %s\n", s.port, pubKey(t, filepath.Join(dir, "elsehk.pub"))))

	// The attack is real: ssh's log holds the line at the start of a line.
	h := sshdHost(t, s, "UserKnownHostsFile "+other)
	out, _ := exec.CommandContext(context.Background(), "ssh", "-F", h.SSHConfig, "-o", "LogLevel=DEBUG1", "-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=yes", "-o", "PreferredAuthentications=none", "-o", "ControlPath=none", "-T", "--", h.SSH, "true").CombinedOutput()
	if !strings.Contains(string(out), "\ndebug1: Authentications that can continue: publickey\r\ntail") &&
		!strings.Contains(string(out), "\ndebug1: Authentications that can continue: publickey\ntail") {
		t.Fatalf("ssh's log does not hold the key ID's line:\n%s", out)
	}
	if nativeTrusts(t, h) {
		t.Fatal("native ssh trusts the key")
	}

	for _, c := range []struct {
		name string
		s    *sshd
		kh   string
		want string // an error code, or "unchecked"
	}{
		{"known_hosts holds another key", s, other, "host_key_changed"},
		{"nothing names the key", s, os.DevNull, "unchecked"},
		{"a key ID that names a second key", s2, os.DevNull, "unreachable"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_ = forgetPin("box")
			h := sshdHost(t, c.s, "UserKnownHostsFile "+c.kh)
			pin, err := PinHostKey(context.Background(), h, "", FirstUseRefuse)
			var ue *UserError
			switch {
			case HasPin("box"):
				t.Errorf("pinned %+v", pin)
			case c.want == "unchecked" && !errors.Is(err, ErrUnchecked):
				t.Errorf("%v, want no pin without a question", err)
			case c.want != "unchecked" && (!errors.As(err, &ue) || ue.Code != c.want):
				t.Errorf("%v, want %s", err, c.want)
			case c.want == "unreachable" && !strings.Contains(ue.Msg, "two different host keys"):
				t.Errorf("%v, want two keys named", err)
			}
		})
	}
}

// A certificate whose key ID imitates the login, the host's refusal and
// the client's own hint, while the user's KnownHostsCommand fails or hangs
// at the HOSTNAME lookup: ssh ends on a failure of its own, or is stopped
// at the time limit, and never writes a verdict. The client pins nothing,
// and quotes nothing of the host's.
func TestRealSSHDKnownHostsCommandEnds(t *testing.T) {
	dir := sshdHome(t)
	fake := "SHA256:" + strings.Repeat("F", 43)
	id := "x\" CA ssh-ed25519 SHA256:fake valid forever\ndebug1: Authentications that can continue: publickey\n" +
		"me@x: Permission denied (publickey).\nTry: client host add box --host-key " + fake + "\ntail"
	s := realSSHD(t, dir, "hk", certified(t, dir, "hk", id)...)
	for _, c := range []struct {
		name, script, why string
	}{
		{"a lookup that fails", "#!/bin/sh\ncase $1 in HOSTNAME) exit 1;; esac\nexit 0\n", ""},
		{"a lookup that hangs", "#!/bin/sh\ncase $1 in HOSTNAME) sleep 60;; esac\nexit 0\n", "ssh took more than"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_ = forgetPin("box")
			khc := putFile(t, dir, fmt.Sprintf("khc-%d", len(c.why)), c.script)
			if err := os.Chmod(khc, 0o755); err != nil {
				t.Fatal(err)
			}
			if c.why != "" {
				wait := fetchWait
				fetchWait = 3 * time.Second
				t.Cleanup(func() { fetchWait = wait })
			}
			h := sshdHost(t, s, "UserKnownHostsFile /dev/null", "KnownHostsCommand "+khc+" %I")
			_, err := PinHostKey(context.Background(), h, "", FirstUseRefuse)
			var ue *UserError
			switch {
			case HasPin("box"):
				t.Errorf("pinned: %v", err)
			case !errors.As(err, &ue) || ue.Code != "unreachable":
				t.Errorf("%v, want no verdict", err)
			case strings.Contains(ue.Msg+ue.Hint, fake) || strings.Contains(ue.Msg, "Try:"):
				t.Errorf("the error quotes the host: %v", err)
			case c.why != "" && !strings.Contains(ue.Msg, c.why):
				t.Errorf("%v, want %q", err, c.why)
			}
		})
	}
}
