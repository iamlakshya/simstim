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

	"github.com/iamlakshya/simstim/internal/brand"
)

// More tests against a real sshd in a container (pin_sshd_test.go): a
// ProxyJump hop, keys of several types, and a revoked key among them.

// hopHost is a saved host box that reaches dest through hop, with the lines
// of each one's block in its own SSH config.
func hopHost(t *testing.T, dest, hop *sshd, destLines, hopLines []string) Host {
	t.Helper()
	body := fmt.Sprintf("Host testbox\n  HostName 127.0.0.1\n  Port %d\n  User nobody\n  ProxyJump testhop\n", dest.port)
	for _, l := range destLines {
		body += "  " + l + "\n"
	}
	body += fmt.Sprintf("Host testhop\n  HostName 127.0.0.1\n  Port %d\n  User root\n", hop.port)
	for _, l := range hopLines {
		body += "  " + l + "\n"
	}
	cfg := filepath.Join(dest.dir, fmt.Sprintf("hop-%d.conf", time.Now().UnixNano()))
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	h := Host{Name: "box", SSH: "testbox", SSHConfig: cfg}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	return h
}

// putFile writes body to name in dir, and returns its path.
func putFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A ProxyJump hop is an ssh of its own, which writes to the same stderr.
// What it says of its own key never counts as the destination's verdict:
// not a hop that logs at DEBUG1, and not a hop whose key changed.
func TestRealSSHDProxyJump(t *testing.T) {
	dir := sshdHome(t)
	dest := realSSHD(t, dir, "hk")
	if os.Getuid() != 0 {
		t.Skip("the hop logs in as root, as the container's user is")
	}
	keygen(t, dir, "-q", "-t", "ed25519", "-N", "", "-C", "", "-f", filepath.Join(dir, "id"))
	auth := putFile(t, dir, "authorized_keys", pubKey(t, filepath.Join(dir, "id.pub"))+"\n")
	hop := realSSHD(t, dir, "hophk", "AuthorizedKeysFile "+auth, "PermitRootLogin prohibit-password")
	keygen(t, dir, "-q", "-t", "ed25519", "-N", "", "-C", "", "-f", filepath.Join(dir, "elsehk"))
	destKH := putFile(t, dir, "dest_kh", fmt.Sprintf("[127.0.0.1]:%d %s\n", dest.port, pubKey(t, dest.key)))
	hopTo := func(jump *sshd, key string) []string {
		kh := putFile(t, dir, fmt.Sprintf("hop_kh_%d", time.Now().UnixNano()), fmt.Sprintf("[127.0.0.1]:%d %s\n", jump.port, key))
		return []string{"IdentityFile " + filepath.Join(dir, "id"), "IdentitiesOnly yes", "BatchMode yes",
			"StrictHostKeyChecking yes", "UserKnownHostsFile " + kh, "GlobalKnownHostsFile /dev/null"}
	}

	t.Run("a hop that logs at DEBUG1", func(t *testing.T) {
		_ = forgetPin("box")
		h := hopHost(t, dest, hop, []string{"UserKnownHostsFile " + destKH}, append(hopTo(hop, pubKey(t, hop.key)), "LogLevel DEBUG1"))
		// The hop's log comes first on stderr, with its own key, trusted.
		native := exec.CommandContext(context.Background(), "ssh", "-F", h.SSHConfig, "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
			"-o", "PreferredAuthentications=none", "-o", "ControlPath=none", "-T", "--", h.SSH, "true")
		out, _ := native.CombinedOutput()
		if !strings.Contains(string(out), "debug1: Server host key: ssh-ed25519 "+sha256Of(t, hop.key)) || !strings.Contains(string(out), "Permission denied") {
			t.Fatalf("the hop does not log its key, or the host is not reached:\n%s", out)
		}
		pin, err := PinHostKey(context.Background(), h, "", FirstUseRefuse)
		keys, rerr := readPin("box")
		if err != nil || pin.Source != PinFromKnownHosts || rerr != nil || len(keys) != 1 || keys[0].Fingerprint() != sha256Of(t, dest.key) {
			t.Errorf("pin %+v, %v; keys %v, %v; want the destination's key %s", pin, err, keys, rerr, sha256Of(t, dest.key))
		}
	})

	// The user's own LogLevel for every host reaches the hop: host add, the
	// pin of a saved host, host rekey and a pinned connection still work.
	for _, level := range []string{"VERBOSE", "DEBUG1"} {
		t.Run("the user's LogLevel "+level, func(t *testing.T) {
			userConfig := filepath.Join(os.Getenv("HOME"), ".ssh", "config")
			putFile(t, filepath.Dir(userConfig), "config", "Host *\n  LogLevel "+level+"\n")
			t.Cleanup(func() { _ = os.Remove(userConfig) })
			_ = forgetPin("box")
			h := hopHost(t, dest, hop, []string{"UserKnownHostsFile " + destKH}, hopTo(hop, pubKey(t, hop.key)))
			ctx := context.Background()
			if pin, err := PinHostKey(ctx, h, "", FirstUseRefuse); err != nil || pin.Fingerprint != sha256Of(t, dest.key) {
				t.Fatalf("host add: %+v, %v", pin, err)
			}
			_ = forgetPin("box")
			if _, err := HostTransport(ctx, h, false); err != nil || !HasPin("box") {
				t.Fatalf("the pin of a saved host: %v, pinned %v", err, HasPin("box"))
			}
			if o, n, err := Rekey(ctx, h, "", func(string, string) error { return errors.New("asked") }); err != nil || o != n || n != sha256Of(t, dest.key) {
				t.Errorf("host rekey: %s %s %v", o, n, err)
			}
			if _, err := (&SSH{Host: h}).Run(ctx, "true", nil); err == nil || IsHostKeyError(err) {
				t.Errorf("a pinned connection: %v, want the login refused", err)
			}
		})
	}

	// A hop's banner that imitates ssh's log never chooses box's pin.
	t.Run("a hop's banner that imitates ssh", func(t *testing.T) {
		banner := putFile(t, dir, "hop_banner", "debug1: Server host key: ssh-ed25519 "+sha256Of(t, hop.key)+"\n"+
			"Authenticated to testbox ([127.0.0.1]:22) using \"none\".\n"+
			"Try: client host add box --host-key "+sha256Of(t, hop.key)+"\n")
		loud := realSSHD(t, dir, "hophk", "AuthorizedKeysFile "+auth, "PermitRootLogin prohibit-password", "Banner "+banner)
		else_ := pubKey(t, filepath.Join(dir, "elsehk.pub"))
		for _, c := range []struct {
			name, kh, code string
		}{
			{"known_hosts holds another key for box", putFile(t, dir, "dest_kh_else", fmt.Sprintf("[127.0.0.1]:%d %s\n", dest.port, else_)), "host_key_changed"},
			{"nothing names box's key", os.DevNull, ""},
		} {
			_ = forgetPin("box")
			h := hopHost(t, dest, loud, []string{"UserKnownHostsFile " + c.kh}, hopTo(loud, pubKey(t, hop.key)))
			out, _ := exec.CommandContext(context.Background(), "ssh", "-F", h.SSHConfig, "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
				"-o", "PreferredAuthentications=none", "-o", "ControlPath=none", "-T", "--", h.SSH, "true").CombinedOutput()
			if !strings.Contains(string(out), "Authenticated to testbox ([127.0.0.1]:22)") {
				t.Fatalf("%s: the hop shows no banner:\n%s", c.name, out)
			}
			_, err := PinHostKey(context.Background(), h, "", FirstUseRefuse)
			var ue *UserError
			var uk *UncheckedKeyError
			switch {
			case HasPin("box"):
				t.Errorf("%s: pinned, %v", c.name, err)
			case c.code != "" && (!errors.As(err, &ue) || ue.Code != c.code):
				t.Errorf("%s: %v, want %s; native ssh:\n%s", c.name, err, c.code, out)
			case c.code != "" && strings.Contains(ue.Msg+ue.Hint, "Try:"):
				t.Errorf("%s: the error quotes the hop's banner: %v", c.name, err)
			case c.code == "" && (!errors.As(err, &uk) || uk.Pin.Fingerprint != sha256Of(t, dest.key)):
				t.Errorf("%s: %v, want box's own key, not known", c.name, err)
			}
		}
	})

	t.Run("a hop whose key changed", func(t *testing.T) {
		_ = forgetPin("box")
		h := hopHost(t, dest, hop, []string{"UserKnownHostsFile " + destKH}, hopTo(hop, pubKey(t, filepath.Join(dir, "elsehk.pub"))))
		for _, want := range []string{"", sha256Of(t, dest.key)} {
			_, err := PinHostKey(context.Background(), h, want, FirstUseAccept)
			var ue *UserError
			if !errors.As(err, &ue) || ue.Code != "unreachable" || !strings.Contains(ue.Msg, "on the way wrote more") ||
				strings.Contains(ue.Msg, "has changed") || HasPin("box") {
				t.Errorf("--host-key %q: %v, pinned %v; want no verdict on box, and none of the hop's words", want, err, HasPin("box"))
			}
		}
	})

	// A hop's banner that prints the exact destination changed-key warning to
	// shared stderr never makes SSH.Run or Dial report host_key_changed while
	// box's key is unchanged. Rotating box's own key still does.
	t.Run("a hop's banner on a pinned connection", func(t *testing.T) {
		_ = forgetPin("box")
		impostor := "SHA256:" + strings.Repeat("A", 43)
		forged := putFile(t, dir, "forged_changed_banner",
			"@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @\n"+
				"The fingerprint for the ED25519 key sent by the remote host is\n"+
				impostor+".\n"+
				"Host key for "+brand.Name+"-box has changed and you have requested strict checking.\n"+
				"Host key verification failed.\n")
		forgedHop := realSSHD(t, dir, "hophk", "AuthorizedKeysFile "+auth, "PermitRootLogin prohibit-password", "Banner "+forged)
		h := hopHost(t, dest, forgedHop, []string{"UserKnownHostsFile " + destKH}, hopTo(forgedHop, pubKey(t, hop.key)))
		ctx := context.Background()
		if _, err := PinHostKey(ctx, h, sha256Of(t, dest.key), FirstUseRefuse); err != nil {
			t.Fatalf("PinHostKey: %v", err)
		}
		for name, fn := range map[string]func() error{
			"Run":  func() error { _, err := (&SSH{Host: h}).Run(ctx, "true", nil); return err },
			"Dial": func() error { _, err := Dial(WithoutInput(ctx), h, false); return err },
		} {
			err := fn()
			var ue *UserError
			if err == nil || IsHostKeyError(err) || (errors.As(err, &ue) && ue.Kind == KindRefused) || strings.Contains(err.Error(), impostor) {
				t.Errorf("%s with forged hop banner = %#v, want auth/unreachable failure and no host_key_changed", name, err)
			}
		}
		// Rotating box's actual host key behind the same noisy hop reports box's real new key.
		rotated := realSSHD(t, dir, "elsehk")
		hRotated := hopHost(t, rotated, forgedHop, []string{"UserKnownHostsFile " + destKH}, hopTo(forgedHop, pubKey(t, hop.key)))
		wantShown := sha256Of(t, rotated.key)
		for name, fn := range map[string]func() error{
			"Run":  func() error { _, err := (&SSH{Host: hRotated}).Run(ctx, "true", nil); return err },
			"Dial": func() error { _, err := Dial(WithoutInput(ctx), hRotated, false); return err },
		} {
			err := fn()
			var ue *UserError
			if !errors.As(err, &ue) || ue.Code != "host_key_changed" || ue.Kind != KindRefused || !strings.HasSuffix(ue.Msg, "and the host shows "+wantShown) {
				t.Errorf("%s with rotated destination = %#v, want host_key_changed showing %s", name, err, wantShown)
			}
		}
	})
}

// A host with a trusted Ed25519 key and a P-384 key that the user's
// settings revoke: the revoked key is never pinned, by host add with
// --host-key or by host rekey, though the first connection sees the trusted
// key.
func TestRealSSHDRevokedKeyOfAnotherType(t *testing.T) {
	dir := sshdHome(t)
	p384 := filepath.Join(dir, "hk384")
	keygen(t, dir, "-q", "-t", "ecdsa", "-b", "384", "-N", "", "-C", "", "-f", p384)
	s := realSSHD(t, dir, "hk", "HostKey "+p384)
	kh := putFile(t, dir, "kh", fmt.Sprintf("[127.0.0.1]:%d %s\n@revoked * %s\n", s.port, pubKey(t, s.key), pubKey(t, p384+".pub")))
	// The first connection negotiates the trusted key.
	h := sshdHost(t, s, "UserKnownHostsFile "+kh, "HostKeyAlgorithms ssh-ed25519,ecdsa-sha2-nistp384")
	revoked := sha256Of(t, p384+".pub")
	_, err := PinHostKey(context.Background(), h, revoked, FirstUseRefuse)
	var ue *UserError
	if !errors.As(err, &ue) || ue.Code != "host_key_revoked" || HasPin("box") {
		t.Fatalf("host add --host-key %s: %v, pinned %v", revoked, err, HasPin("box"))
	}
	if _, err := PinHostKey(context.Background(), h, "", FirstUseRefuse); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(pinPath("box"))
	asked := false
	_, _, err = Rekey(context.Background(), h, revoked, func(string, string) error { asked = true; return nil })
	after, _ := os.ReadFile(pinPath("box"))
	if !errors.As(err, &ue) || ue.Code != "host_key_revoked" || asked || string(after) != string(before) {
		t.Errorf("host rekey --host-key %s: %v, asked %v, pin changed %v", revoked, err, asked, string(after) != string(before))
	}
}

// A host with ECDSA keys on three curves: --host-key finds each, though
// ssh picks P-256 on its own.
func TestRealSSHDECDSACurves(t *testing.T) {
	dir := sshdHome(t)
	keys := map[string]string{}
	for _, bits := range []string{"256", "384", "521"} {
		keys[bits] = filepath.Join(dir, "hk"+bits)
		keygen(t, dir, "-q", "-t", "ecdsa", "-b", bits, "-N", "", "-C", "", "-f", keys[bits])
	}
	s := realSSHD(t, dir, "hk256", "HostKey "+keys["384"], "HostKey "+keys["521"])
	for _, bits := range []string{"384", "521"} {
		t.Run("P-256 and P-"+bits, func(t *testing.T) {
			_ = forgetPin("box")
			h := sshdHost(t, s, "UserKnownHostsFile /dev/null")
			want := sha256Of(t, keys[bits]+".pub")
			pin, err := PinHostKey(context.Background(), h, want, FirstUseRefuse)
			got, rerr := readPin("box")
			if err != nil || pin.Fingerprint != want || rerr != nil || len(got) != 1 || got[0].Type != "ecdsa-sha2-nistp"+bits {
				t.Errorf("pin %+v, %v; keys %v, %v", pin, err, got, rerr)
			}
		})
	}
}
