package sshserve

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

func TestParseTarget(t *testing.T) {
	sess, host, err := ParseTarget("auth-fix.my-vps." + brand.Name)
	if err != nil || sess != "auth-fix" || host != "my-vps" {
		t.Fatalf("got (%q, %q, %v)", sess, host, err)
	}
	bad := []string{
		"bad",
		"a.b.other",
		"Auth.box." + brand.Name,
		"a$b.box." + brand.Name,
		"a;id.box." + brand.Name,
		"a.b.c." + brand.Name,
		".box." + brand.Name,
	}
	for _, b := range bad {
		if _, _, err := ParseTarget(b); err == nil {
			t.Errorf("ParseTarget(%q) succeeded, want error", b)
		}
	}
}

func TestHostKeyAndConfigLifecycle(t *testing.T) {
	home := t.TempDir()
	state := filepath.Join(home, "state")
	key1, err := EnsureHostKey(state)
	if err != nil {
		t.Fatalf("EnsureHostKey: %v", err)
	}
	key2, err := EnsureHostKey(state)
	if err != nil {
		t.Fatalf("second EnsureHostKey: %v", err)
	}
	if !bytes.Equal(key1.PublicKey().Marshal(), key2.PublicKey().Marshal()) {
		t.Error("host key was regenerated instead of reused")
	}
	cfgPath, khPath, err := WriteFiles(home, key1, "")
	if err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	cfgBytes, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(cfgBytes), "ProxyCommand "+brand.Name+" ssh-proxy %n") {
		t.Errorf("config = %s", cfgBytes)
	}
	khBytes, _ := os.ReadFile(khPath)
	if !strings.HasPrefix(string(khBytes), "*."+brand.Name+" ssh-ed25519 ") {
		t.Errorf("known_hosts = %s", khBytes)
	}

	// Pre-populate ~/.ssh/config and ensure Include goes at the top, idempotently.
	_, _, _, userCfg := ConfigPaths(home)
	if err := os.WriteFile(userCfg, []byte("Host other\n  User bob\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := EnsureInclude(home)
	if err != nil || !changed {
		t.Fatalf("EnsureInclude = %v, %v", changed, err)
	}
	changed2, err := EnsureInclude(home)
	if err != nil || changed2 {
		t.Fatalf("second EnsureInclude = %v, %v", changed2, err)
	}
	gotUser, _ := os.ReadFile(userCfg)
	if !strings.HasPrefix(string(gotUser), IncludeLine()+"\n") || !strings.Contains(string(gotUser), "Host other") {
		t.Errorf("user config = %q", gotUser)
	}

	// Remove cleans up both.
	remInc, remDir, err := Remove(home)
	if err != nil || !remInc || !remDir {
		t.Fatalf("Remove = (%v, %v, %v)", remInc, remDir, err)
	}
	gotAfter, _ := os.ReadFile(userCfg)
	if strings.Contains(string(gotAfter), IncludeLine()) || !strings.Contains(string(gotAfter), "Host other") {
		t.Errorf("after Remove = %q", gotAfter)
	}
	if _, err := os.Stat(cfgPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("config file still exists: %v", err)
	}
}

func TestCheckOpenSSHDetectsVulnerableSSH(t *testing.T) {
	dir := t.TempDir()
	// Fake ssh that exits 0 (accepts probe$x) => must be refused.
	badSSH := filepath.Join(dir, "bad-ssh")
	if err := os.WriteFile(badSSH, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckOpenSSH(context.Background(), badSSH); !errors.Is(err, ErrOldOpenSSH) {
		t.Errorf("bad ssh = %v, want ErrOldOpenSSH", err)
	}
	// Fake ssh that rejects probe$x with non-zero => must pass.
	goodSSH := filepath.Join(dir, "good-ssh")
	if err := os.WriteFile(goodSSH, []byte("#!/bin/sh\nexit 255\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckOpenSSH(context.Background(), goodSSH); err != nil {
		t.Errorf("good ssh = %v, want nil", err)
	}
	// Missing ssh binary must fail rather than passing the probe.
	if err := CheckOpenSSH(context.Background(), filepath.Join(dir, "missing-ssh")); err == nil || errors.Is(err, ErrOldOpenSSH) {
		t.Errorf("missing ssh = %v, want launch error", err)
	}
	// Cancelled context must fail rather than passing the probe.
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := CheckOpenSSH(cctx, goodSSH); err == nil || errors.Is(err, ErrOldOpenSSH) {
		t.Errorf("cancelled check = %v, want context error", err)
	}
}

type fakeExec struct {
	mu      sync.Mutex
	args    api.SSHExecArgs
	resized [][2]int
	signals []string
	inR     *io.PipeReader
	inW     *io.PipeWriter
	outR    *io.PipeReader
	outW    *io.PipeWriter
	errR    *io.PipeReader
	errW    *io.PipeWriter
	exit    int
}

func newFakeExec(args api.SSHExecArgs, out, errText string, code int) *fakeExec {
	ir, iw := io.Pipe()
	or, ow := io.Pipe()
	er, ew := io.Pipe()
	fe := &fakeExec{args: args, inR: ir, inW: iw, outR: or, outW: ow, errR: er, errW: ew, exit: code}
	go func() {
		defer ow.Close()
		defer ew.Close()
		if args.Command == "echo-stdin" {
			b, _ := io.ReadAll(ir)
			_, _ = ow.Write(b)
		} else {
			_, _ = ow.Write([]byte(out))
		}
		_, _ = ew.Write([]byte(errText))
	}()
	return fe
}

func (f *fakeExec) Stdin() io.WriteCloser { return f.inW }
func (f *fakeExec) Stdout() io.Reader     { return f.outR }
func (f *fakeExec) Stderr() io.Reader     { return f.errR }
func (f *fakeExec) Resize(c, r int) error {
	f.mu.Lock()
	f.resized = append(f.resized, [2]int{c, r})
	f.mu.Unlock()
	return nil
}
func (f *fakeExec) Signal(name string) error {
	f.mu.Lock()
	f.signals = append(f.signals, name)
	f.mu.Unlock()
	return nil
}
func (f *fakeExec) Wait() (int, error) { return f.exit, nil }
func (f *fakeExec) Close() error {
	_ = f.inR.Close()
	_ = f.inW.Close()
	return nil
}

type fakeBackend struct {
	sftpDir string
	mu      sync.Mutex
	last    *fakeExec
	dialed  []string
}

type pipeRWC struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (p pipeRWC) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p pipeRWC) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p pipeRWC) CloseWrite() error           { return p.w.Close() }
func (p pipeRWC) Close() error {
	_ = p.w.Close()
	return p.r.Close()
}

func (b *fakeBackend) OpenSFTP(_ context.Context) (io.ReadWriteCloser, error) {
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	srv, err := sftp.NewServer(pipeRWC{r: c2sR, w: s2cW}, sftp.WithServerWorkingDirectory(b.sftpDir))
	if err != nil {
		return nil, err
	}
	go func() {
		_ = srv.Serve()
		_ = s2cW.Close()
	}()
	return pipeRWC{r: s2cR, w: c2sW}, nil
}

func (b *fakeBackend) StartExec(_ context.Context, args api.SSHExecArgs) (ExecStream, error) {
	var fe *fakeExec
	if args.Command == "fail42" {
		fe = newFakeExec(args, "", "boom\n", 42)
	} else {
		fe = newFakeExec(args, "ok-out\n", "ok-err\n", 0)
	}
	b.mu.Lock()
	b.last = fe
	b.mu.Unlock()
	return fe, nil
}

func (b *fakeBackend) DialPort(_ context.Context, host string, port uint32) (io.ReadWriteCloser, error) {
	b.mu.Lock()
	b.dialed = append(b.dialed, host)
	b.mu.Unlock()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	go func() {
		defer ln.Close()
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = io.Copy(c, c)
	}()
	return net.Dial("tcp", ln.Addr().String())
}

func TestServeAllowedAndRefusedRequests(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "note.txt"), []byte("hello-sftp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	key, err := EnsureHostKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakeBackend{sftpDir: work}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srvDone := make(chan error, 1)
	go func() {
		srvConn, aerr := ln.Accept()
		if aerr != nil {
			srvDone <- aerr
			return
		}
		srvDone <- Serve(ctx, srvConn, key, backend)
	}()
	cliConn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	cconn, chans, reqs, err := ssh.NewClientConn(cliConn, "s.h."+brand.Name, &ssh.ClientConfig{
		User:            "agent",
		HostKeyCallback: ssh.FixedHostKey(key.PublicKey()),
	})
	if err != nil {
		t.Fatalf("NewClientConn: %v", err)
	}
	client := ssh.NewClient(cconn, chans, reqs)
	defer client.Close()

	// 1. Disallowed global request (ssh -R / tcpip-forward) fails without hanging.
	if ok, _, err := client.SendRequest("tcpip-forward", true, ssh.Marshal(struct {
		Addr string
		Port uint32
	}{"127.0.0.1", 9999})); err != nil || ok {
		t.Errorf("tcpip-forward = (%v, %v), want (false, nil)", ok, err)
	}

	// 2. Disallowed channel types fail without hanging.
	if _, _, err := client.OpenChannel("direct-streamlocal@openssh.com", nil); err == nil {
		t.Error("direct-streamlocal succeeded, want refusal")
	}
	if _, _, err := client.OpenChannel("unknown-kind", nil); err == nil {
		t.Error("unknown channel succeeded, want refusal")
	}

	// 3. Disallowed direct-tcpip to non-loopback fails.
	if _, err := client.Dial("tcp", "192.0.2.1:80"); err == nil {
		t.Error("non-loopback direct-tcpip succeeded, want refusal")
	}

	// 4. Allowed direct-tcpip to loopback works.
	tc, err := client.Dial("tcp", "127.0.0.1:3000")
	if err != nil {
		t.Fatalf("loopback direct-tcpip: %v", err)
	}
	go func() {
		_, _ = tc.Write([]byte("echo"))
		if cw, ok := tc.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()
	buf := make([]byte, 4)
	if _, err := io.ReadFull(tc, buf); err != nil || string(buf) != "echo" {
		t.Fatalf("loopback echo = %q, %v", buf, err)
	}
	_ = tc.Close()

	// 5. Session disallowed requests (x11, agent forwarding, env, bad subsystem) return false.
	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Setenv("FOO", "bar"); err == nil {
		t.Error("SendEnv succeeded, want refusal")
	}
	if ok, err := sess.SendRequest("x11-req", true, nil); err != nil || ok {
		t.Errorf("x11-req = (%v, %v), want false", ok, err)
	}
	if ok, err := sess.SendRequest("auth-agent-req@openssh.com", true, nil); err != nil || ok {
		t.Errorf("auth-agent-req = (%v, %v), want false", ok, err)
	}
	if err := sess.RequestSubsystem("bogus"); err == nil {
		t.Error("bogus subsystem succeeded, want refusal")
	}
	// Allowed exec with exit code 42.
	var stderr bytes.Buffer
	sess.Stderr = &stderr
	err = sess.Run("fail42")
	var exitErr *ssh.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitStatus() != 42 || stderr.String() != "boom\n" {
		t.Errorf("exec fail42 = (%v, stderr=%q), want exit 42", err, stderr.String())
	}
	_ = sess.Close()

	// 6. SFTP subsystem works end-to-end with pkg/sftp.
	sc, err := sftp.NewClient(client)
	if err != nil {
		t.Fatalf("sftp.NewClient: %v", err)
	}
	rf, err := sc.Open("note.txt")
	if err != nil {
		t.Fatalf("sftp open: %v", err)
	}
	got, err := io.ReadAll(rf)
	_ = rf.Close()
	if err != nil || string(got) != "hello-sftp\n" {
		t.Fatalf("sftp read = %q, %v", got, err)
	}
	_ = sc.Close()

	// 7. Rate limit: opening > MaxChannelsPerSecond in one burst gets rejected.
	rejected := false
	for range MaxChannelsPerSecond + 10 {
		ch, reqCh, err := client.OpenChannel("session", nil)
		if err != nil {
			rejected = true
			break
		}
		go ssh.DiscardRequests(reqCh)
		_ = ch.Close()
	}
	if !rejected {
		t.Error("channel open burst was not rate-limited")
	}

	_ = client.Close()
	cancel()
	<-srvDone
}
