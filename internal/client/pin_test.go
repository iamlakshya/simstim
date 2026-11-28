package client

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/iamlakshya/simstim/internal/brand"
)

func genEd25519Signer(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func genECDSASigner(t *testing.T) ssh.Signer {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func genRSASigner(t *testing.T) ssh.Signer {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// testSSHServer is an in-process OpenSSH-compatible server for testing real
// system ssh host-key verification and pinning without needing sshd.
type testSSHServer struct {
	addr    string
	port    int
	ln      net.Listener
	mu      sync.RWMutex
	signers []ssh.Signer
	pubOut  string // returned when a client runs a command containing ssh_host_
	conns   map[net.Conn]struct{}
}

func newTestSSHServer(t *testing.T, signers ...ssh.Signer) *testSSHServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &testSSHServer{
		addr:    ln.Addr().String(),
		port:    ln.Addr().(*net.TCPAddr).Port,
		ln:      ln,
		signers: signers,
		conns:   make(map[net.Conn]struct{}),
	}
	t.Cleanup(func() {
		_ = ln.Close()
		srv.closeConns()
	})
	go srv.serve()
	return srv
}

func (s *testSSHServer) closeConns() {
	s.mu.Lock()
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	clear(s.conns)
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

func (s *testSSHServer) setSigners(signers ...ssh.Signer) {
	s.mu.Lock()
	s.signers = signers
	s.mu.Unlock()
	// Close live connections (such as a ControlMaster) as a reinstalled
	// host or restarted sshd does, so the next ssh runs a fresh handshake.
	s.closeConns()
}

func (s *testSSHServer) setPubOut(out string) {
	s.mu.Lock()
	s.pubOut = out
	s.mu.Unlock()
}

func (s *testSSHServer) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns[c] = struct{}{}
		s.mu.Unlock()
		go s.handle(c)
	}
}

func (s *testSSHServer) handle(raw net.Conn) {
	defer func() {
		s.mu.Lock()
		delete(s.conns, raw)
		s.mu.Unlock()
		_ = raw.Close()
	}()
	s.mu.RLock()
	signers := append([]ssh.Signer(nil), s.signers...)
	pubOut := s.pubOut
	s.mu.RUnlock()

	cfg := &ssh.ServerConfig{
		// Accept any public key auth for login tests, while rejecting "none"
		// auth so noAuthOpts stops cleanly after host-key verification.
		PublicKeyCallback: func(_ ssh.ConnMetadata, _ ssh.PublicKey) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	for _, sig := range signers {
		cfg.AddHostKey(sig)
	}
	sc, chans, reqs, err := ssh.NewServerConn(raw, cfg)
	if err != nil {
		return
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		ch, requests, err := nc.Accept()
		if err != nil {
			return
		}
		go func(ch ssh.Channel, reqs <-chan *ssh.Request) {
			defer ch.Close()
			for r := range reqs {
				if r.Type != "exec" {
					if r.WantReply {
						_ = r.Reply(false, nil)
					}
					continue
				}
				var payload struct{ Cmd string }
				_ = ssh.Unmarshal(r.Payload, &payload)
				if r.WantReply {
					_ = r.Reply(true, nil)
				}
				if strings.Contains(payload.Cmd, "ssh_host_") && pubOut != "" {
					_, _ = ch.Write([]byte(pubOut))
				} else if strings.Contains(payload.Cmd, "echo ok") {
					_, _ = ch.Write([]byte("ok\n"))
				}
				_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
				return
			}
		}(ch, requests)
	}
}

// writeClientKey creates a throwaway Ed25519 client identity for real ssh tests.
func writeClientKey(t *testing.T, dir string) string {
	t.Helper()
	keyPath := filepath.Join(dir, "id_test")
	out, err := execCommand("ssh-keygen", "-t", "ed25519", "-N", "", "-f", keyPath, "-q").CombinedOutput()
	if err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}
	return keyPath
}

func TestNormalizeHostKey(t *testing.T) {
	sig := genEd25519Signer(t)
	fp := ssh.FingerprintSHA256(sig.PublicKey())

	got, err := NormalizeHostKey("  " + fp + "=  ")
	if err != nil || got != fp {
		t.Fatalf("NormalizeHostKey(%q) = %q, %v; want %q", fp, got, err, fp)
	}
	for _, bad := range []string{"", "MD5:aa:bb", "SHA256:", "SHA256:short", "SHA256:!!!invalid!!!"} {
		_, err := NormalizeHostKey(bad)
		var ue *UserError
		if !errors.As(err, &ue) || ue.Kind != KindUsage || ue.Code != "bad_host_key" {
			t.Errorf("NormalizeHostKey(%q) = %v, want bad_host_key usage error", bad, err)
		}
	}
}

func TestPinPathRejectsBadNames(t *testing.T) {
	sshDirs(t)
	for _, bad := range []string{"", "../escape", "a/b", "box.", ".hidden", "a b"} {
		if _, err := PinPath(bad); err == nil {
			t.Errorf("PinPath(%q) succeeded, want error", bad)
		}
	}
	p, err := PinPath("my-box_1")
	if err != nil || filepath.Base(p) != "known_hosts-my-box_1" {
		t.Fatalf("PinPath(my-box_1) = %q, %v", p, err)
	}
}

// TestPinArgsWinOverSSHConfig checks against real `ssh -G` that a pinned host's
// command-line options override Permissive ~/.ssh/config and Lima-style lines,
// even when XDG_CONFIG_HOME contains a space.
func TestPinArgsWinOverSSHConfig(t *testing.T) {
	userSSH := realSSH(t)
	spaced := filepath.Join(t.TempDir(), "my config")
	t.Setenv("XDG_CONFIG_HOME", spaced)

	sig := genEd25519Signer(t)
	if err := writePinFile(context.Background(), "box", []ssh.PublicKey{sig.PublicKey()}); err != nil {
		t.Fatal(err)
	}
	pinPath, err := PinPath("box")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(userSSH, "config"),
		"Host *\n"+
			"  StrictHostKeyChecking no\n"+
			"  UserKnownHostsFile /dev/null\n"+
			"  GlobalKnownHostsFile /tmp/fake_global_known_hosts\n"+
			"  VerifyHostKeyDNS yes\n"+
			"  NoHostAuthenticationForLocalhost yes\n"+
			"  UpdateHostKeys yes\n"+
			"  CheckHostIP yes\n"+
			"  FingerprintHash md5\n"+
			"  LogLevel QUIET\n")

	s := &SSH{Host: Host{Name: "box", SSH: "box.invalid"}, Interactive: true}
	cmd, err := s.Command(context.Background(), "true", "-T")
	if err != nil {
		t.Fatal(err)
	}
	fwd, err := s.Forward(context.Background(), []ForwardPair{{Local: 45678, RemoteHost: "127.0.0.1", Remote: 3000}})
	if err != nil {
		t.Fatal(err)
	}
	cfgVals, err := s.ConfigValues(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for name, vals := range map[string]map[string][]string{
		"Command":      sshValues(t, cmd.Args[1:]),
		"Forward":      sshValues(t, fwd.Args[1:]),
		"ConfigValues": cfgVals,
	} {
		// OpenSSH 10 prints true/false and SHA256; older versions print yes/no and sha256.
		allowed := map[string][]string{
			"hostkeyalias":                     {HostKeyAlias("box")},
			"userknownhostsfile":               {pinPath},
			"stricthostkeychecking":            {"yes", "true"},
			"updatehostkeys":                   {"no", "false"},
			"globalknownhostsfile":             {"none"},
			"verifyhostkeydns":                 {"no", "false"},
			"nohostauthenticationforlocalhost": {"no", "false"},
			"checkhostip":                      {"no", "false"},
			"fingerprinthash":                  {"sha256", "SHA256"},
			"loglevel":                         {"ERROR", "error"},
		}
		for k, opts := range allowed {
			got := vals[k]
			if len(got) != 1 || !slices.Contains(opts, got[0]) {
				t.Errorf("%s: %s = %q, want one of %q", name, k, got, opts)
			}
		}
	}
}

// TestPinHostKeyRealSSH exercises PinHostKey, changed-key refusal, host rekey,
// revocation, --host-key matching across key types, known_hosts trust, first-use
// pinning, and ForgetHost against real OpenSSH ssh and an in-process SSH server.
func TestPinHostKeyRealSSH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("real OpenSSH test requires POSIX ssh and ssh-keygen")
	}
	userSSH := realSSH(t)
	keyPath := writeClientKey(t, userSSH)

	ed1 := genEd25519Signer(t)
	ed2 := genEd25519Signer(t)
	rsa1 := genRSASigner(t)
	ec1 := genECDSASigner(t)
	fpEd1 := ssh.FingerprintSHA256(ed1.PublicKey())
	fpEd2 := ssh.FingerprintSHA256(ed2.PublicKey())
	fpRSA1 := ssh.FingerprintSHA256(rsa1.PublicKey())

	srv := newTestSSHServer(t, ed1, rsa1, ec1)
	srv.setPubOut(string(ssh.MarshalAuthorizedKey(ed1.PublicKey())))

	// Deliberately hostile ~/.ssh/config trying to disable host key checks
	// (like Lima's ssh.config or permissive developer configs).
	permissiveCfg := fmt.Sprintf(
		"Host testbox\n"+
			"  HostName 127.0.0.1\n"+
			"  Port %d\n"+
			"  User testuser\n"+
			"  IdentityFile %q\n"+
			"  IdentitiesOnly yes\n"+
			"  StrictHostKeyChecking no\n"+
			"  UserKnownHostsFile %s\n"+
			"  NoHostAuthenticationForLocalhost yes\n",
		srv.port, keyPath, filepath.Join(userSSH, "known_hosts"),
	)
	writeFile(t, filepath.Join(userSSH, "config"), permissiveCfg)

	ctx := context.Background()
	h := Host{Name: "box", SSH: "testbox"}

	// 1. Unknown host with input off -> exit code 4 (KindNeedsAnswer), no pin left.
	if _, err := PinHostKey(ctx, h, PinOptions{}); err == nil {
		t.Fatal("PinHostKey succeeded for unknown host with input off")
	} else {
		var ue *UserError
		if !errors.As(err, &ue) || ue.Kind != KindNeedsAnswer {
			t.Fatalf("unknown host err = %+v, want KindNeedsAnswer", err)
		}
	}
	if HasPin("box") {
		t.Fatal("pin file was written on failed PinHostKey")
	}

	// 2. --host-key mismatch -> KindRefused (exit 5), no pin left.
	if _, err := PinHostKey(ctx, h, PinOptions{HostKey: fpEd2}); err == nil {
		t.Fatal("PinHostKey matched wrong --host-key")
	} else {
		var ue *UserError
		if !errors.As(err, &ue) || ue.Kind != KindRefused || ue.Code != "host_key_mismatch" {
			t.Fatalf("wrong --host-key err = %+v, want KindRefused host_key_mismatch", err)
		}
	}
	if HasPin("box") {
		t.Fatal("pin file was written on --host-key mismatch")
	}

	// 3. --host-key matching a non-default key type (RSA) -> PinFromFlag.
	res, err := PinHostKey(ctx, h, PinOptions{HostKey: fpRSA1})
	if err != nil {
		t.Fatalf("PinHostKey(--host-key RSA): %v", err)
	}
	if res.Source != PinFromFlag || res.Fingerprint != fpRSA1 || !res.Created {
		t.Fatalf("PinHostKey RSA result = %+v", res)
	}
	// Existing pin is never replaced by PinHostKey; conflicting --host-key is refused.
	if _, err := PinHostKey(ctx, h, PinOptions{HostKey: fpEd1}); err == nil {
		t.Fatal("PinHostKey allowed replacing existing pin")
	} else {
		var ue *UserError
		if !errors.As(err, &ue) || ue.Kind != KindRefused {
			t.Fatalf("existing pin conflict = %+v, want KindRefused", err)
		}
	}
	// Clean up to test Ed25519 pin and rotation next.
	if err := RemovePin(ctx, "box"); err != nil {
		t.Fatal(err)
	}

	// 4. --host-key matching Ed25519 -> PinFromFlag, connection works.
	res, err = PinHostKey(ctx, h, PinOptions{HostKey: fpEd1})
	if err != nil {
		t.Fatalf("PinHostKey(--host-key Ed25519): %v", err)
	}
	if res.Source != PinFromFlag || res.Fingerprint != fpEd1 {
		t.Fatalf("res = %+v", res)
	}
	out, err := (&SSH{Host: h}).Run(ctx, "echo ok", nil)
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("SSH.Run with matching pin = %q, %v", out, err)
	}

	// 5. Rotate server host key -> SSH.Run MUST refuse with exit code 5 (KindRefused)
	//    and name BOTH old and new SHA256 fingerprints, despite StrictHostKeyChecking no!
	srv.setSigners(ed2)
	(&SSH{Host: h}).ExitMaster(ctx)
	_, err = (&SSH{Host: h}).Run(ctx, "echo ok", nil)
	var ue *UserError
	if !errors.As(err, &ue) || ue.Kind != KindRefused || ue.Code != "host_key_changed" {
		t.Fatalf("SSH.Run after key rotation = %+v, want KindRefused host_key_changed", err)
	}
	if !strings.Contains(ue.Msg, fpEd1) || !strings.Contains(ue.Msg, fpEd2) {
		t.Errorf("changed key message %q lacks both %s and %s", ue.Msg, fpEd1, fpEd2)
	}
	if !strings.Contains(ue.Hint, brand.Name+" host rekey box") {
		t.Errorf("changed key hint %q lacks host rekey box", ue.Hint)
	}

	// 6. FetchRekeyKeys + ApplyRekey replaces the pin; connection works again!
	keys, newFPs, err := FetchRekeyKeys(ctx, h, fpEd2)
	if err != nil || len(newFPs) != 1 || newFPs[0] != fpEd2 {
		t.Fatalf("FetchRekeyKeys = %v, %v", newFPs, err)
	}
	if _, err := ApplyRekey(ctx, h, keys); err != nil {
		t.Fatalf("ApplyRekey: %v", err)
	}
	out, err = (&SSH{Host: h}).Run(ctx, "echo ok", nil)
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("SSH.Run after rekey = %q, %v", out, err)
	}

	// 7. ForgetHost removes the pin file and wrapper config.
	if err := SaveHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	if err := ForgetHost(ctx, "box"); err != nil {
		t.Fatalf("ForgetHost: %v", err)
	}
	if HasPin("box") {
		t.Error("ForgetHost left pin file behind")
	}

	// 8. Trusted from user's known_hosts -> PinFromKnownHosts.
	khAddr := fmt.Sprintf("[127.0.0.1]:%d", srv.port)
	srv.setPubOut(string(ssh.MarshalAuthorizedKey(ed2.PublicKey())))
	writeFile(t, filepath.Join(userSSH, "known_hosts"), knownhosts.Line([]string{khAddr}, ed2.PublicKey())+"\n")
	res, err = PinHostKey(ctx, h, PinOptions{})
	if err != nil || res.Source != PinFromKnownHosts || res.Fingerprint != fpEd2 {
		t.Fatalf("PinHostKey from known_hosts = %+v, %v", res, err)
	}
	_ = RemovePin(ctx, "box")

	// 9. Changed key in user's known_hosts -> refused even with AcceptFirstUse=true.
	writeFile(t, filepath.Join(userSSH, "known_hosts"), knownhosts.Line([]string{khAddr}, ed1.PublicKey())+"\n")
	if _, err := PinHostKey(ctx, h, PinOptions{AcceptFirstUse: true}); err == nil {
		t.Fatal("PinHostKey accepted a host whose key differs from user known_hosts")
	} else if !errors.As(err, &ue) || ue.Kind != KindRefused || ue.Code != "host_key_changed" {
		t.Fatalf("changed in known_hosts = %+v, want KindRefused host_key_changed", err)
	}

	// 10. Revoked key (@revoked) in user's known_hosts -> refused by PinHostKey,
	//     PinHostKey(--host-key), and FetchRekeyKeys.
	writeFile(t, filepath.Join(userSSH, "known_hosts"), "@revoked * "+strings.TrimSpace(string(ssh.MarshalAuthorizedKey(ed2.PublicKey())))+"\n")
	for name, fn := range map[string]func() error{
		"PinHostKey":            func() error { _, err := PinHostKey(ctx, h, PinOptions{AcceptFirstUse: true}); return err },
		"PinHostKey --host-key": func() error { _, err := PinHostKey(ctx, h, PinOptions{HostKey: fpEd2}); return err },
		"FetchRekeyKeys":        func() error { _, _, err := FetchRekeyKeys(ctx, h, ""); return err },
	} {
		err := fn()
		if !errors.As(err, &ue) || ue.Kind != KindRefused || ue.Code != "host_key_revoked" {
			t.Errorf("%s on revoked key = %+v, want KindRefused host_key_revoked", name, err)
		}
	}

	// 11. AcceptFirstUse (e.g. --yes or host create lima) with empty known_hosts -> PinOnFirstUse.
	writeFile(t, filepath.Join(userSSH, "known_hosts"), "")
	res, err = PinHostKey(ctx, h, PinOptions{AcceptFirstUse: true})
	if err != nil || res.Source != PinOnFirstUse || res.Fingerprint != fpEd2 {
		t.Fatalf("PinHostKey AcceptFirstUse = %+v, %v", res, err)
	}
	_ = RemovePin(ctx, "box")

	// 12. Pre-H3 saved host gets pinned automatically on HostTransport.
	writeFile(t, filepath.Join(userSSH, "known_hosts"), knownhosts.Line([]string{khAddr}, ed2.PublicKey())+"\n")
	if err := SaveHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	if _, err := HostTransport(ctx, h, false); err != nil {
		t.Fatalf("HostTransport on pre-H3 host: %v", err)
	}
	if !HasPin("box") {
		t.Error("HostTransport did not pin pre-H3 saved host")
	}

	// 13. scanHostKeyViaSSH directly (used for ProxyJump/ProxyCommand hosts).
	keys, err = scanHostKeyViaSSH(ctx, h, "")
	if err != nil || len(keys) == 0 || ssh.FingerprintSHA256(keys[0]) != fpEd2 {
		t.Fatalf("scanHostKeyViaSSH = %v, %v", fingerprintsOf(keys), err)
	}
}

func TestForwardErrorChangedHostKey(t *testing.T) {
	sshDirs(t)
	sig1 := genEd25519Signer(t)
	sig2 := genEd25519Signer(t)
	fp1 := ssh.FingerprintSHA256(sig1.PublicKey())
	fp2 := ssh.FingerprintSHA256(sig2.PublicKey())
	if err := writePinFile(context.Background(), "box", []ssh.PublicKey{sig1.PublicKey()}); err != nil {
		t.Fatal(err)
	}
	// Run a real command that exits 255 to get an authentic *exec.ExitError.
	exit255 := execCommand("sh", "-c", "exit 255").Run()
	stderr := "@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\n" +
		"@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @\n" +
		"The fingerprint for the ED25519 key sent by the remote host is\n" +
		fp2 + ".\nHost key verification failed.\n"
	err := ForwardError(Host{Name: "box", SSH: "box"}, exit255, stderr)
	var ue *UserError
	if !errors.As(err, &ue) || ue.Kind != KindRefused || ue.Code != "host_key_changed" ||
		!strings.Contains(ue.Msg, fp1) || !strings.Contains(ue.Msg, fp2) {
		t.Fatalf("ForwardError = %+v", err)
	}
}

func execCommand(name string, args ...string) *exec.Cmd {
	return exec.CommandContext(context.Background(), name, args...)
}
