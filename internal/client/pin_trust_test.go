package client

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

// The trust check reads ssh's own log, which -E sends to a file of its
// own. A ProxyJump hop writes to stderr: its log at DEBUG1, and a warning
// about its own key, never count as the destination's verdict.
func TestTrustReadsOnlyTheDestination(t *testing.T) {
	dir := fakeSSH(t)
	dest, hop := newKey(t, "ssh-ed25519"), newKey(t, "ssh-ed25519")
	h := Host{Name: "box", SSH: "box.example"}
	ctx := context.Background()
	put(t, dir, "hop-stderr", strings.ReplaceAll(trustLog(hop, "trusted"), "box.example", "hop.example")+
		"Authenticated to hop.example ([192.0.2.9]:22) using \"publickey\".\r\n")
	put(t, dir, "trust-log", trustLog(dest, "unknown"))
	got, err := trustHostKey(ctx, h, "")
	if err != nil || got.kind != trustUnknown || got.fp != dest.Fingerprint() {
		t.Errorf("a hop that logs at DEBUG1: %+v, %v; want box's key, not known", got, err)
	}
	if calls := calls(t, dir, "trust"); len(calls) != 1 || !strings.Contains(calls[0], " -E ") {
		t.Errorf("the trust check has no log of its own: %q", calls)
	}
	// A hop whose key changed: ssh never reaches box. The error says so in
	// the client's own words, and quotes nothing that the hop wrote.
	put(t, dir, "hop-stderr", "@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\r\n"+
		"@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @\r\nHost key verification failed.\r\n"+fakeTry)
	put(t, dir, "trust-log", "kex_exchange_identification: Connection closed by remote host\r\nConnection closed by UNKNOWN port 65535\r\n")
	_, err = trustHostKey(ctx, h, "")
	var ue *UserError
	if !errors.As(err, &ue) || ue.Code != "unreachable" || !strings.Contains(ue.Msg, "on the way wrote more") || quotesHost(ue) {
		t.Errorf("a hop whose key changed: %v", err)
	}
	if _, err := PinHostKey(ctx, h, "", FirstUseAccept); !errors.As(err, &ue) || ue.Code != "unreachable" || HasPin("box") {
		t.Errorf("PinHostKey behind a hop whose key changed: %v, pinned %v", err, HasPin("box"))
	}
}

// fakeTry is a line that a host or a hop writes to look like the client's
// own hint, with a key of its own.
var fakeTry = "Try: client host add box --host-key SHA256:" + strings.Repeat("F", 43) + "\r\n"

// quotesHost reports whether an error quotes what a host or hop wrote.
func quotesHost(ue *UserError) bool {
	return strings.Contains(ue.Msg+ue.Hint, strings.Repeat("F", 43)) || strings.Contains(ue.Msg, "Try:") ||
		strings.Contains(ue.Msg, "REMOTE HOST IDENTIFICATION")
}

// The client's errors never quote what a host writes once the connection
// starts: a certificate's key ID, a host's message, or a hop's banner,
// which could hold a false Try line with a key of the host's own.
func TestNoHostTextInErrors(t *testing.T) {
	dir := fakeSSH(t)
	k := newKey(t, "ssh-ed25519")
	h := Host{Name: "box", SSH: "box.example"}
	ctx := context.Background()
	var ue *UserError
	// The trust check: a key ID with a fake Try line, then ssh fails.
	// Its recovery hint directs the user to ssh -v, never --host-key.
	put(t, dir, "trust-log", strings.Replace(injectedEnd(k, "ssh: KnownHostsCommand failed\r\n"), "tail\"", fakeTry+"tail\"", 1))
	if _, err := trustHostKey(ctx, h, ""); !errors.As(err, &ue) || quotesHost(ue) ||
		strings.Contains(ue.Hint, "--host-key") || !strings.HasPrefix(ue.Hint, "ssh -v ") {
		t.Errorf("the trust check: %#v", err)
	}
	// The connection that reads the key: a banner with a fake Try line.
	put(t, dir, "fetch-stderr", fakeTry)
	if _, err := fetchHostKey(ctx, h, false, ""); !errors.As(err, &ue) || quotesHost(ue) || !strings.Contains(ue.Msg, "Permission denied") {
		t.Errorf("the key's connection: %v", err)
	}
	// A pinned connection that ssh ends with 255.
	pinFor(t, "box")
	put(t, dir, "stderr", fakeTry+"Connection closed by 192.0.2.7 port 22\r\n")
	if _, err := (&SSH{Host: h}).Run(ctx, "true", nil); !errors.As(err, &ue) || quotesHost(ue) || !strings.Contains(ue.Msg, "closed before the key exchange") {
		t.Errorf("a connection: %v", err)
	}
	// A hop banner on stderr that forges the exact destination changed-key
	// warning never turns an auth failure into host_key_changed.
	forgedHop := "@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @\r\n" +
		"The fingerprint for the ED25519 key sent by the remote host is\r\n" +
		"SHA256:" + strings.Repeat("A", 43) + ".\r\n" +
		"Host key for " + brand.Name + "-box has changed and you have requested strict checking.\r\n" +
		"Host key verification failed.\r\n" + fakeTry
	put(t, dir, "hop-stderr", forgedHop)
	put(t, dir, "stderr", "me@box.example: Permission denied (publickey).\r\n")
	if _, err := (&SSH{Host: h}).Run(ctx, "true", nil); err == nil || IsHostKeyError(err) || strings.Contains(err.Error(), "Try:") || strings.Contains(err.Error(), strings.Repeat("F", 43)) {
		t.Errorf("forged hop warning on Run: %#v", err)
	}
	if _, err := Dial(WithoutInput(ctx), h, false); err == nil || IsHostKeyError(err) || strings.Contains(err.Error(), "Try:") || strings.Contains(err.Error(), strings.Repeat("F", 43)) {
		t.Errorf("forged hop warning on Dial: %#v", err)
	}

	// Timeout or cancellation killing ssh (exitCode == -1) never quotes stderr.
	put(t, dir, "stderr", "")
	put(t, dir, "hop-stderr", "Try: "+brand.Name+" host add box --host-key SHA256:"+strings.Repeat("F", 43)+"\r\n")
	put(t, dir, "hang", "")
	tctx, cancel := context.WithTimeout(ctx, 80*time.Millisecond)
	_, rerr := (&SSH{Host: h}).Run(tctx, "true", nil)
	cancel()
	if !errors.Is(rerr, context.DeadlineExceeded) || strings.Contains(rerr.Error(), "Try:") || strings.Contains(rerr.Error(), strings.Repeat("F", 43)) {
		t.Errorf("timeout on Run quoted stderr: %v", rerr)
	}
	dctx, dcancel := context.WithTimeout(ctx, 80*time.Millisecond)
	_, derr := Dial(WithoutInput(dctx), h, false)
	dcancel()
	if !errors.Is(derr, context.DeadlineExceeded) || strings.Contains(derr.Error(), "Try:") || strings.Contains(derr.Error(), strings.Repeat("F", 43)) {
		t.Errorf("timeout on Dial quoted stderr: %v", derr)
	}
	_ = os.Remove(filepath.Join(dir, "hang"))

	// Signal death without context cancellation never quotes stderr either.
	put(t, dir, "kill-self", "")
	for name, fn := range map[string]func() error{
		"Run":  func() error { _, err := (&SSH{Host: h}).Run(ctx, "true", nil); return err },
		"Dial": func() error { _, err := Dial(WithoutInput(ctx), h, false); return err },
	} {
		err := fn()
		if !errors.As(err, &ue) || ue.Code != "unreachable" || quotesHost(ue) || strings.Contains(err.Error(), strings.Repeat("F", 43)) {
			t.Errorf("signal death on %s: %#v", name, err)
		}
	}
	_ = os.Remove(filepath.Join(dir, "kill-self"))

	// Even on remote exit 1, standalone forged Try lines are stripped.
	put(t, dir, "exit-code", "1")
	put(t, dir, "hop-stderr", "remote command failed\r\n  Try: "+brand.Name+" host add box --host-key SHA256:"+strings.Repeat("F", 43)+"\r\n")
	for name, fn := range map[string]func() error{
		"Run":  func() error { _, err := (&SSH{Host: h}).Run(ctx, "true", nil); return err },
		"Dial": func() error { _, err := Dial(WithoutInput(ctx), h, false); return err },
	} {
		err := fn()
		if err == nil || !strings.Contains(err.Error(), "remote command failed") || strings.Contains(err.Error(), "Try:") || strings.Contains(err.Error(), strings.Repeat("F", 43)) {
			t.Errorf("remote exit 1 on %s: %v", name, err)
		}
	}
}

// Every reader of `ssh -G` runs it through sshG: the runs that ssh sees
// are the runs that sshG counts, for each of the four readers.
func TestEveryReaderUsesSSHG(t *testing.T) {
	dir := fakeSSH(t)
	put(t, dir, "values", values("ask"))
	knowsMu.Lock()
	kept := knows
	knows = map[string]bool{}
	knowsMu.Unlock()
	t.Cleanup(func() { knowsMu.Lock(); knows = kept; knowsMu.Unlock() })
	h := Host{Name: "box", SSH: "box.example"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	if err := writeTestPin("box", []hostKey{newKey(t, "ssh-ed25519")}); err != nil {
		t.Fatal(err)
	}
	s := &SSH{Host: h}
	ctx := context.Background()
	runsOfG := func() int {
		b, _ := os.ReadFile(filepath.Join(dir, "log"))
		n := 0
		for _, l := range strings.Split(string(b), "\n") {
			if strings.Contains(" "+l+" ", " -G ") {
				n++
			}
		}
		return n
	}
	for _, r := range []struct {
		name string
		read func() error
	}{
		{"option detection", func() error { _, err := knowsOption("VisualHostKey=no"); return err }},
		{"host key settings", func() error { _, err := hostKeyValues(ctx, h); return err }},
		{"ConfigValues", func() error { _, err := s.ConfigValues(ctx); return err }},
		{"Forward", func() error { _, err := s.Forward(ctx, nil); return err }},
	} {
		name, read := r.name, r.read
		ran, counted := runsOfG(), sshGRuns.Load()
		err := read()
		ran, counted = runsOfG()-ran, sshGRuns.Load()-counted
		if ran == 0 || int64(ran) != counted {
			t.Errorf("%s: ssh -G ran %d times, sshG %d (%v)", name, ran, counted, err)
		}
	}
}

// host rekey --host-key never pins a key that the user's settings revoke,
// though the host shows a trusted key of another type first.
func TestRekeyRefusesARevokedKeyOfAnotherType(t *testing.T) {
	ctx := context.Background()
	dir := fakeSSH(t)
	h := Host{Name: "box", SSH: "box.example"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	old, k, rsa := newKey(t, "ssh-ed25519"), newKey(t, "ssh-ed25519"), newKey(t, "ssh-rsa")
	if err := writeTestPin("box", []hostKey{old}); err != nil {
		t.Fatal(err)
	}
	shows(t, dir, k, rsa)
	put(t, dir, "trust-log", trustLog(k, "trusted"))
	put(t, dir, "trust-log-rsa-sha2-512", trustLog(rsa, "revoked"))
	asked := false
	confirm := func(string, string) error { asked = true; return nil }
	_, _, err := Rekey(ctx, h, rsa.Fingerprint(), confirm)
	var ue *UserError
	if !errors.As(err, &ue) || ue.Code != "host_key_revoked" || asked {
		t.Errorf("a revoked key of another type: %v, asked %v", err, asked)
	}
	if keys, _ := readPin("box"); len(keys) != 1 || keys[0].Fingerprint() != old.Fingerprint() {
		t.Errorf("the pin changed: %v", keys)
	}
	// The same key, not known: host rekey asks, and pins it.
	put(t, dir, "trust-log-rsa-sha2-512", trustLog(rsa, "unknown"))
	if _, _, err := Rekey(ctx, h, rsa.Fingerprint(), confirm); err != nil || !asked {
		t.Fatalf("a key of another type: %v, asked %v", err, asked)
	}
	if keys, _ := readPin("box"); len(keys) != 1 || keys[0].Fingerprint() != rsa.Fingerprint() {
		t.Errorf("the pin: %v", keys)
	}
}

// A host with no SSH target, such as a Docker container, has no shared
// connection: ExitMaster builds no command line and writes no file.
func TestExitMasterWithoutSSH(t *testing.T) {
	sshDirs(t)
	(&SSH{Host: Host{Name: "box", Machine: &Machine{Provider: "docker", ID: "c1"}}}).ExitMaster(context.Background())
	if files := tree(t, ConfigDir()); len(files) != 0 {
		t.Errorf("ExitMaster wrote %v", files)
	}
}

// Even when ssh exits 0 with a complete trusted log just as the trust check's
// deadline expires, trustHostKey fails closed on tctx.Err().
func TestTrustTimeoutGuardRejectsCleanExit(t *testing.T) {
	sshDirs(t)
	k := newKey(t, "ssh-ed25519")
	h := Host{Name: "box", SSH: "box.example"}
	prevWait, prevRun := fetchWait, runTrustCmd
	fetchWait = 40 * time.Millisecond
	t.Cleanup(func() { fetchWait, runTrustCmd = prevWait, prevRun })
	denied := "me@box.example: Permission denied (publickey).\r\n"
	okLog := strings.TrimSuffix(trustLog(k, "trusted"), denied) +
		"Authenticated to box.example ([192.0.2.7]:22) using \"none\".\r\n"
	runTrustCmd = func(cmd *exec.Cmd) error {
		for i := 0; i+1 < len(cmd.Args); i++ {
			if cmd.Args[i] == "-E" {
				if err := os.WriteFile(cmd.Args[i+1], []byte(okLog), 0o600); err != nil {
					return err
				}
			}
		}
		time.Sleep(80 * time.Millisecond)
		return nil // ProcessState == nil -> exit code 0
	}
	_, err := trustHostKey(context.Background(), h, "")
	var ue *UserError
	if !errors.As(err, &ue) || ue.Code != "unreachable" || !strings.Contains(ue.Msg, "ssh took more than ") {
		t.Fatalf("trustHostKey on expired tctx with exit 0 = %#v, want timeout refusal", err)
	}
}

// Decision record 0035 names the exact 32-hex HostFileKey("box") and does not
// claim --host-key recovers from a failed trust check.
func TestDecision0035MatchesHostFileKey(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "contributing", "decisions", "0035-pinned-host-keys.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	want := "known_hosts-" + HostFileKey("box")
	if !strings.Contains(doc, want) {
		t.Errorf("0035 lacks %q", want)
	}
	if strings.Contains(doc, "known_hosts-box-26f8567f`") {
		t.Error("0035 still contains the 8-hex filename example")
	}
	if strings.Contains(doc, "can recover with `--host-key`") {
		t.Error("0035 still claims --host-key bypasses a failed trust check")
	}
}
