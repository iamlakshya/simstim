//go:build !windows

package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

// These tests run the real ssh against a real sshd on 127.0.0.1, inside a
// Linux container that has openssh-server, with PIN_SSHD=1. They never
// reach another machine, and every file they write is in a temporary
// folder, HOME included. Elsewhere they skip.

// sshd is a running sshd of a test.
type sshd struct {
	dir  string
	port int
	key  string // the public key file of its host key
}

// realSSHD starts sshd with the host key keyName, made in dir, with the
// extra lines of its config. They come first, so they win over the lines
// of realSSHD's own: sshd takes the first value of most keywords. It skips
// without PIN_SSHD=1.
func realSSHD(t *testing.T, dir, keyName string, extra ...string) *sshd {
	t.Helper()
	if os.Getenv("PIN_SSHD") != "1" {
		t.Skip("set PIN_SSHD=1 in a container with openssh-server to run the tests against a real sshd")
	}
	if _, err := os.Stat("/usr/sbin/sshd"); err != nil {
		t.Skip("no sshd")
	}
	if err := os.MkdirAll("/run/sshd", 0o755); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(dir, keyName)
	if _, err := os.Stat(key); err != nil {
		keygen(t, dir, "-q", "-t", "ed25519", "-N", "", "-C", "", "-f", key)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	cfg := filepath.Join(dir, fmt.Sprintf("sshd-%d.conf", port))
	lines := append(slices.Clone(extra),
		fmt.Sprintf("Port %d", port), "ListenAddress 127.0.0.1", "HostKey "+key,
		"PidFile "+filepath.Join(dir, fmt.Sprintf("sshd-%d.pid", port)), "UsePAM no",
		"PasswordAuthentication no", "KbdInteractiveAuthentication no", "AuthorizedKeysFile none", "StrictModes no",
	)
	// A test makes many connections that log in to nothing. An sshd of 9.8
	// or later would then refuse 127.0.0.1 for a while (PerSourcePenalties).
	quiet := append(slices.Clone(lines), "PerSourcePenalties no")
	if err := os.WriteFile(cfg, []byte(strings.Join(quiet, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if exec.CommandContext(context.Background(), "/usr/sbin/sshd", "-t", "-f", cfg).Run() != nil {
		// An older sshd, which has no penalties, and no such keyword.
		if err := os.WriteFile(cfg, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.CommandContext(context.Background(), "/usr/sbin/sshd", "-D", "-e", "-f", cfg)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	for end := time.Now().Add(10 * time.Second); ; {
		c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			_ = c.Close()
			break
		}
		if time.Now().After(end) {
			t.Fatalf("sshd did not start: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return &sshd{dir: dir, port: port, key: key + ".pub"}
}

// keygen runs ssh-keygen with HOME in the test's own folder.
func keygen(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "ssh-keygen", args...)
	cmd.Env = append(os.Environ(), "HOME="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen %v: %v\n%s", args, err, out)
	}
}

// pubKey is the "type base64" of the public key file at path.
func pubKey(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(string(b))
	return f[0] + " " + f[1]
}

// sshdHost is a saved host that reaches s, with the lines of its own SSH
// config after its address.
func sshdHost(t *testing.T, s *sshd, lines ...string) Host {
	t.Helper()
	return sshdHostAt(t, s, "127.0.0.1", lines...)
}

// sshdHostAt is sshdHost with the HostName name.
func sshdHostAt(t *testing.T, s *sshd, name string, lines ...string) Host {
	t.Helper()
	cfg := filepath.Join(s.dir, fmt.Sprintf("host-%d.conf", time.Now().UnixNano()))
	body := fmt.Sprintf("Host testbox\n  HostName %s\n  Port %d\n  User nobody\n", name, s.port)
	for _, l := range lines {
		body += "  " + l + "\n"
	}
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	h := Host{Name: "box", SSH: "testbox", SSHConfig: cfg}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	return h
}

// nativeTrusts asks the real ssh, with the host's own config and nothing
// of the client's, whether it trusts the host's key: it reaches the login,
// which the host refuses.
func nativeTrusts(t *testing.T, h Host) bool {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "ssh", "-F", h.SSHConfig, "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
		"-o", "PreferredAuthentications=none", "-o", "ControlPath=none", "-o", "UpdateHostKeys=no", "-T", "--", h.SSH, "true")
	out, _ := cmd.CombinedOutput()
	switch {
	case strings.Contains(string(out), "Permission denied"):
		return true
	case strings.Contains(string(out), "Host key verification failed"):
		return false
	}
	t.Fatalf("native ssh said neither: %s", out)
	return false
}

// sshdHome gives the test a HOME and client folders of its own, and
// returns its folder for sshd.
func sshdHome(t *testing.T) string {
	t.Helper()
	sshDirs(t)
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	return dir
}

// The user's own settings decide trust, as the real ssh decides it, in the
// cases where a matcher of the client's own could differ.
func TestRealSSHDTrust(t *testing.T) {
	dir := sshdHome(t)
	s := realSSHD(t, dir, "hk")
	key := pubKey(t, s.key)
	at := fmt.Sprintf("[127.0.0.1]:%d", s.port)
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	big := strings.Repeat("filler.example "+key+"\n", 17<<20/len("filler.example "+key+"\n")+1)
	hashed := write("hashed", at+" "+key+"\n")
	keygen(t, dir, "-H", "-f", hashed)
	t.Chdir(dir)
	for _, c := range []struct {
		name  string
		lines []string
		want  string // "trusted", "unknown" or "revoked"
	}{
		{"a port wildcard", []string{"UserKnownHostsFile " + write("wild", "[127.0.0.1]:* "+key+"\n")}, "trusted"},
		{"a revocation in a second relative file", []string{"UserKnownHostsFile kh1 kh2"}, "revoked"},
		{"a hashed entry", []string{"UserKnownHostsFile " + hashed}, "trusted"},
		{"a file over 16 MiB", []string{"UserKnownHostsFile " + write("big", big+at+" "+key+"\n")}, "trusted"},
		{"a KnownHostsCommand", []string{"UserKnownHostsFile /dev/null", "KnownHostsCommand /bin/cat " + write("khc", at+" "+key+"\n")}, "trusted"},
		{"nothing known", []string{"UserKnownHostsFile /dev/null"}, "unknown"},
	} {
		t.Run(c.name, func(t *testing.T) {
			write("kh1", at+" "+key+"\n")
			write("kh2", "@revoked * "+key+"\n")
			_ = forgetPin("box")
			h := sshdHost(t, s, c.lines...)
			native := nativeTrusts(t, h)
			pin, err := PinHostKey(context.Background(), h, "", FirstUseRefuse)
			var ue *UserError
			switch c.want {
			case "trusted":
				if !native || err != nil || pin.Source != PinFromKnownHosts {
					t.Errorf("native trusts: %v; the client: %+v, %v", native, pin, err)
				}
			case "unknown":
				if native || !errors.Is(err, ErrUnchecked) {
					t.Errorf("native trusts: %v; the client: %v", native, err)
				}
			case "revoked":
				if native || !errors.As(err, &ue) || ue.Code != "host_key_revoked" {
					t.Errorf("native trusts: %v; the client: %v", native, err)
				}
			}
		})
	}
}

// A host that only a certificate authority vouches for: OpenSSH trusts it,
// and the client pins the host's own key, which the certificate signs.
func TestRealSSHDCertificateAuthority(t *testing.T) {
	dir := sshdHome(t)
	keygen(t, dir, "-q", "-t", "ed25519", "-N", "", "-C", "", "-f", filepath.Join(dir, "ca"))
	keygen(t, dir, "-q", "-t", "ed25519", "-N", "", "-C", "", "-f", filepath.Join(dir, "hk"))
	keygen(t, dir, "-q", "-s", filepath.Join(dir, "ca"), "-I", "box", "-h", "-n", "127.0.0.1", filepath.Join(dir, "hk.pub"))
	s := realSSHD(t, dir, "hk", "HostCertificate "+filepath.Join(dir, "hk-cert.pub"))
	kh := filepath.Join(dir, "kh")
	if err := os.WriteFile(kh, []byte("@cert-authority * "+pubKey(t, filepath.Join(dir, "ca.pub"))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := sshdHost(t, s, "UserKnownHostsFile "+kh)
	if !nativeTrusts(t, h) {
		t.Fatal("native ssh does not trust the certificate")
	}
	pin, err := PinHostKey(context.Background(), h, "", FirstUseRefuse)
	keys, rerr := readPin("box")
	if err != nil || pin.Source != PinFromKnownHosts || rerr != nil || len(keys) != 1 || keys[0].Type+" "+strings.Fields(keys[0].line(""))[1] != pubKey(t, s.key) {
		t.Errorf("pin %+v, %v; keys %v, %v", pin, err, keys, rerr)
	}
}

// The pin's message and exit code do not depend on the user's config, its
// file never changes under the client, and a host's banner cannot imitate
// ssh's message.
func TestRealSSHDPinnedConnections(t *testing.T) {
	dir := sshdHome(t)
	if err := os.WriteFile(filepath.Join(dir, "banner"), []byte("Host key for "+brand.Name+"-box has changed and you have requested strict checking.\nHost key verification failed.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// CheckHostIP needs an address that is not the loopback, and a
	// HostName that is a name: ssh turns it off otherwise. With it on,
	// ssh would add the address to the pin.
	ip, name := lanAddress(t)
	s := realSSHD(t, dir, "hk", "Banner "+filepath.Join(dir, "banner"), "ListenAddress "+ip)
	user := "LogLevel QUIET\nFingerprintHash md5\nCheckHostIP yes\nStrictHostKeyChecking no\nUserKnownHostsFile /dev/null\n"
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), ".ssh", "config"), []byte(user), 0o600); err != nil {
		t.Fatal(err)
	}
	h := sshdHostAt(t, s, name)
	if _, err := PinHostKey(context.Background(), h, "", FirstUseAccept); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(pinPath("box"))
	if err != nil {
		t.Fatal(err)
	}
	// The same key, a banner that imitates ssh, and a refused login.
	_, err = (&SSH{Host: h}).Run(context.Background(), "true", nil)
	if err == nil || IsHostKeyError(err) {
		t.Errorf("the banner: %v", err)
	}
	if after, _ := os.ReadFile(pinPath("box")); string(after) != string(before) {
		t.Errorf("the pin changed under the client:\n%s", after)
	}
	// Another key at the same address: exit code 5, with its SHA256.
	other := realSSHD(t, dir, "hk2")
	h = sshdHost(t, other)
	_, err = (&SSH{Host: h}).Run(context.Background(), "true", nil)
	var ue *UserError
	shown := sha256Of(t, other.key)
	if !errors.As(err, &ue) || ue.Code != "host_key_changed" || ue.Kind != KindRefused || !strings.HasSuffix(ue.Msg, "and the host shows "+shown) {
		t.Errorf("a changed key: %#v, want the host to show %s", err, shown)
	}
}

// lanAddress is an IPv4 address of this machine that is not the loopback,
// and a name of this machine's that leads to it.
func lanAddress(t *testing.T) (ip, name string) {
	t.Helper()
	name, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	addrs, _ := net.LookupHost(name)
	for _, a := range addrs {
		if p := net.ParseIP(a); p != nil && p.To4() != nil && !p.IsLoopback() {
			return a, name
		}
	}
	t.Skipf("%s has no address but the loopback", name)
	return "", ""
}

// sha256Of is the SHA256 fingerprint of the public key file at path.
func sha256Of(t *testing.T, path string) string {
	t.Helper()
	_, k, ok := parseKeyLine("x " + pubKey(t, path))
	if !ok {
		t.Fatalf("%s holds no key", path)
	}
	return k.Fingerprint()
}

// VerboseLog writes debug1 lines from its single locked pin snapshot (so a
// concurrent ForgetHost never drops the pin's options), and ForgetHost stops
// any shared ControlMaster so a command prepared before the forget cannot
// execute over multiplexing.
func TestRealSSHDVerboseLogAndForgetMaster(t *testing.T) {
	if os.Getenv("PIN_SSHD") != "1" {
		t.Skip("set PIN_SSHD=1 in a container with openssh-server to run the tests against a real sshd")
	}
	if os.Getuid() != 0 {
		t.Skip("the container test logs in as root")
	}
	dir := sshdHome(t)
	keygen(t, dir, "-q", "-t", "ed25519", "-N", "", "-C", "", "-f", filepath.Join(dir, "id"))
	auth := putFile(t, dir, "authorized_keys", pubKey(t, filepath.Join(dir, "id.pub"))+"\n")
	s := realSSHD(t, dir, "hk_login", "AuthorizedKeysFile "+auth, "PermitRootLogin prohibit-password")
	user := "StrictHostKeyChecking no\nUserKnownHostsFile /dev/null\nLogLevel QUIET\n"
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), ".ssh", "config"), []byte(user), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := putFile(t, dir, "root.conf", fmt.Sprintf(
		"Host testbox\n  HostName 127.0.0.1\n  Port %d\n  User root\n  IdentityFile %s\n  IdentitiesOnly yes\n  BatchMode yes\n",
		s.port, filepath.Join(dir, "id")))
	h := Host{Name: "box", SSH: "testbox", SSHConfig: cfg}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := PinHostKey(ctx, h, "", FirstUseAccept); err != nil {
		t.Fatal(err)
	}

	// VerboseLog captures debug1 lines while enforcing the pin.
	vlog, err := (&SSH{Host: h}).VerboseLog(ctx)
	if err != nil || !strings.Contains(string(vlog), "debug1:") || !strings.Contains(string(vlog), brand.Name+"-box") || !strings.Contains(string(vlog), "Authenticated to") {
		t.Fatalf("VerboseLog = %v\n%s", err, vlog)
	}

	// ForgetHost right after VerboseLog's snapshot: VerboseLog keeps the
	// snapshot's pin options, so ssh refuses the missing pin file instead of
	// authenticating with the user's StrictHostKeyChecking=no.
	read := readPinLocked
	t.Cleanup(func() { readPinLocked = read })
	once := true
	readPinLocked = func(ctx context.Context, name string, f func()) error {
		err := read(ctx, name, f)
		if once {
			once = false
			if ferr := ForgetHostContext(ctx, name); ferr != nil {
				t.Errorf("ForgetHost: %v", ferr)
			}
			if _, werr := writeWrapper(h); werr != nil {
				t.Errorf("writeWrapper: %v", werr)
			}
		}
		return err
	}
	vlog2, err := (&SSH{Host: h}).VerboseLog(ctx)
	if err != nil || strings.Contains(string(vlog2), "Authenticated to") || !strings.Contains(string(vlog2), "Host key verification failed") {
		t.Fatalf("VerboseLog after concurrent ForgetHost authenticated or lost pin check: %v\n%s", err, vlog2)
	}
	readPinLocked = read

	// Re-add box, start a shared ControlMaster, prepare a command, then
	// ForgetHost: the master stops and the prepared command fails closed.
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	if _, err := PinHostKey(ctx, h, "", FirstUseAccept); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { (&SSH{Host: h}).ExitMaster(ctx) })
	out, err := (&SSH{Host: h}).Run(ctx, "echo READY", nil)
	if err != nil || strings.TrimSpace(string(out)) != "READY" {
		t.Fatalf("Run READY = %q, %v", out, err)
	}
	if running, _ := (&SSH{Host: h}).CheckMaster(ctx); !running {
		t.Fatal("CheckMaster before ForgetHost = false; want running")
	}
	cmd, err := (&SSH{Host: h}).Command(ctx, "echo PWNED", "-T")
	if err != nil {
		t.Fatal(err)
	}
	if err := ForgetHost("box"); err != nil {
		t.Fatalf("ForgetHost: %v", err)
	}
	if running, _ := (&SSH{Host: h}).CheckMaster(ctx); running {
		t.Fatal("ControlMaster still running after ForgetHost")
	}
	// Even if a case-twin writes config-box back so -F exists, cmd cannot
	// multiplex or connect without the deleted pin.
	if _, err := writeWrapper(h); err != nil {
		t.Fatal(err)
	}
	out, err = cmd.CombinedOutput()
	var ee *exec.ExitError
	if err == nil || !errors.As(err, &ee) || ee.ExitCode() != 255 || strings.Contains(string(out), "PWNED") {
		t.Fatalf("prepared cmd after ForgetHost = %v, out=%q; want exit 255 and no PWNED", err, out)
	}
}
