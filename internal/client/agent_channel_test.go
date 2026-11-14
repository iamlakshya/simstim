//go:build !windows

package client

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/iamlakshya/simstim/internal/agentbridge"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
)

func agentClientEnv(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "ac-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	for key, leaf := range map[string]string{"HOME": "home", "USERPROFILE": "home", "XDG_CONFIG_HOME": "config", "XDG_CACHE_HOME": "cache", "XDG_DATA_HOME": "data", "XDG_STATE_HOME": "state", "XDG_RUNTIME_DIR": "run", "XDG_CONFIG_DIRS": "configs", "XDG_DATA_DIRS": "datas", "GNUPGHOME": "gpg"} {
		dir := filepath.Join(root, leaf)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, dir)
	}
	for _, key := range []string{"SSH_AUTH_SOCK", "SSH_AGENT_PID", "DBUS_SESSION_BUS_ADDRESS", "GPG_AGENT_INFO", "GPG_TTY", "GIT_CONFIG_PARAMETERS"} {
		t.Setenv(key, "")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(root, "gitconfig-system"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	return root
}

type countedChannelAgent struct {
	agent.Agent
	lists, signs atomic.Int32
	bindings     atomic.Int32
}

func (a *countedChannelAgent) List() ([]*agent.Key, error) { a.lists.Add(1); return a.Agent.List() }
func (a *countedChannelAgent) Sign(key ssh.PublicKey, data []byte) (*ssh.Signature, error) {
	a.signs.Add(1)
	return a.Agent.Sign(key, data)
}

func (a *countedChannelAgent) SignWithFlags(key ssh.PublicKey, data []byte, flags agent.SignatureFlags) (*ssh.Signature, error) {
	if flags != 0 {
		return nil, errors.New("generated fixture supports only default signing flags")
	}
	return a.Sign(key, data)
}

func (a *countedChannelAgent) Extension(name string, _ []byte) ([]byte, error) {
	if name != agentFWSessionBind {
		return nil, agent.ErrExtensionUnsupported
	}
	a.bindings.Add(1)
	return []byte{agentFWSuccess}, nil
}

type channelSource struct {
	path    string
	public  ssh.PublicKey
	agent   *countedChannelAgent
	accepts atomic.Int32
}

func newChannelSource(t *testing.T, root string) *channelSource {
	t.Helper()
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: private}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "source.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	source := &channelSource{path: path, public: public, agent: &countedChannelAgent{Agent: keyring}}
	var mu sync.Mutex
	clients := map[net.Conn]bool{}
	var workers sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			source.accepts.Add(1)
			mu.Lock()
			clients[conn] = true
			workers.Add(1)
			mu.Unlock()
			go func() {
				defer workers.Done()
				defer conn.Close()
				_ = agent.ServeAgent(source.agent, conn)
				mu.Lock()
				delete(clients, conn)
				mu.Unlock()
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
		mu.Lock()
		for c := range clients {
			_ = c.Close()
		}
		mu.Unlock()
		workers.Wait()
	})
	return source
}

type channelProc struct {
	net.Conn
	done chan struct{}
	err  error
	stop func() bool
}

func (p *channelProc) Stdin() io.WriteCloser { return p.Conn }
func (p *channelProc) Stdout() io.Reader     { return p.Conn }
func (*channelProc) Stderr() string          { return "" }
func (p *channelProc) Kill()                 { _ = p.Conn.Close() }
func (p *channelProc) Wait() error {
	<-p.done
	if p.stop != nil {
		p.stop()
	}
	return p.err
}

type channelTransport struct {
	combineHello bool
	wrap         func(net.Conn) net.Conn
}

func (t channelTransport) Start(ctx context.Context, remote string) (Proc, error) {
	if !strings.HasSuffix(remote, " bridge-agent") {
		return nil, errors.New("agent mode did not select its fixed helper entry")
	}
	client, server := net.Pipe()
	if t.wrap != nil {
		client = t.wrap(client)
	}
	p := &channelProc{Conn: client, done: make(chan struct{})}
	p.stop = context.AfterFunc(ctx, p.Kill)
	go func() {
		defer close(p.done)
		defer server.Close()
		var out io.WriteCloser = server
		if t.combineHello {
			out = &joinedHelloWriter{Conn: server}
		}
		p.err = host.RunAgentBridge(server, out)
	}()
	return p, nil
}
func (channelTransport) Run(context.Context, string, io.Reader) ([]byte, error) {
	return nil, errors.New("unexpected fixture Run")
}
func (channelTransport) ReachHint() string { return "Use the local channel fixture." }

// joinedHelloWriter puts the normal and link hellos in one pipe Write. The
// identity reader must retain the second frame for the sole link reader.
type joinedHelloWriter struct {
	net.Conn
	first  []byte
	joined bool
}

func (w *joinedHelloWriter) Write(p []byte) (int, error) {
	if w.joined {
		return w.Conn.Write(p)
	}
	if w.first == nil {
		w.first = bytes.Clone(p)
		return len(p), nil
	}
	w.joined = true
	out := append(w.first, p...)
	_, err := w.Conn.Write(out)
	return len(p), err
}

type activationWriter struct {
	net.Conn
	blocked   chan struct{}
	closed    chan struct{}
	once      sync.Once
	closeOnce sync.Once
}

func (w *activationWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(`"action":"activate"`)) {
		w.once.Do(func() { close(w.blocked) })
		<-w.closed
		return 0, net.ErrClosed
	}
	return w.Conn.Write(p)
}
func (w *activationWriter) Close() error {
	w.closeOnce.Do(func() { close(w.closed) })
	return w.Conn.Close()
}

func channelHostFixture(t *testing.T) (Host, agentbridge.Scope, *channelSource) {
	t.Helper()
	root := agentClientEnv(t)
	source := newChannelSource(t, root)
	t.Setenv("SSH_AUTH_SOCK", source.path)
	h := Host{Name: "box", SSH: "box.example"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	pinFor(t, h.Name)
	hostID, err := host.HostID()
	if err != nil {
		t.Fatal(err)
	}
	if err := SetTierContext(t.Context(), h.Name, TierOwn, hostID, ""); err != nil {
		t.Fatal(err)
	}
	lease, err := agentbridge.NewLease()
	if err != nil {
		t.Fatal(err)
	}
	scope := agentbridge.Scope{Session: "abc23def", Lease: lease}
	if err := reserveSandbox(t.Context(), h, SandboxPolicy{SessionID: scope.Session, HostID: hostID, Ceiling: TierOwn, SSHAgent: true, CreatedAt: time.Now().UTC(), request: "generated-channel-fixture"}); err != nil {
		t.Fatal(err)
	}
	manifest := &host.Manifest{Schema: 1, ID: scope.Session, Name: "channel", SSHAgent: true, Cwd: root}
	if err := manifest.Save(); err != nil {
		t.Fatal(err)
	}
	return h, scope, source
}

func bindChannelFixture(t *testing.T, h Host, scope agentbridge.Scope, source *channelSource, transport channelTransport) *attachedAgent {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, err := dial(ctx, h, route{t: transport, agentOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if !conn.agentOnly || conn.agentReader == nil {
		t.Fatal("agent mode did not own the buffered reader")
	}
	if transport.combineHello && conn.agentReader.Buffered() == 0 {
		t.Fatal("fixture did not put the second hello in the retained buffer")
	}
	a, err := bindAgentConn(ctx, conn, scope, source.path, nil)
	if err != nil {
		conn.Close()
		t.Fatal(err)
	}
	// This fixture supplies an already verified native token to isolate
	// channel publication/lifecycle. It is not a native or crypto proof.
	a.binding = fwBinding()
	a.binding.owner, a.binding.scope = conn, scope
	t.Cleanup(a.Close)
	return a
}

func signChannel(t *testing.T, path string, public ssh.PublicKey) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	payload := []byte("generated-agent-channel-signature")
	signature, err := agent.NewClient(conn).Sign(public, payload)
	if err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if err := public.Verify(payload, signature); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	return conn
}

func TestAgentChannelOwnsBufferedHandoffAndNeverDialsBeforeActivation(t *testing.T) {
	h, scope, source := channelHostFixture(t)
	a := bindChannelFixture(t, h, scope, source, channelTransport{combineHello: true})
	if source.accepts.Load() != 0 || source.agent.lists.Load() != 0 || source.agent.signs.Load() != 0 {
		t.Fatal("bind exposed a source connection before activation")
	}
	if _, err := os.Stat(paths.AgentSocket(scope.Session)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bind published its endpoint: %v", err)
	}
	if err := a.Activate(t.Context()); err != nil {
		t.Fatal(err)
	}
	connection := signChannel(t, paths.AgentSocket(scope.Session), source.public)
	if source.agent.bindings.Load() != 1 {
		t.Fatal("fresh source did not receive its forwarding prefix before signing")
	}
	t.Cleanup(func() { _ = connection.Close() })
	grants, err := GrantsForHostContext(t.Context(), h, a.conn.HostID())
	if err != nil || len(grants) != 1 || grants[0].Kind != OutboundSSHAgent || grants[0].Count != 1 {
		t.Fatalf("agent grant=%+v,%v", grants, err)
	}
	a.Close()
	_, err = agent.NewClient(connection).List()
	var timeout net.Error
	if err == nil || errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatalf("closed channel served or hung on an existing agent FD: %v", err)
	}
}

func TestCancelledAgentPublicationRevokesBeforeUnlockingPolicy(t *testing.T) {
	h, scope, source := channelHostFixture(t)
	var blocked *activationWriter
	a := bindChannelFixture(t, h, scope, source, channelTransport{wrap: func(c net.Conn) net.Conn {
		blocked = &activationWriter{Conn: c, blocked: make(chan struct{}), closed: make(chan struct{})}
		return blocked
	}})
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() { result <- a.Activate(ctx) }()
	select {
	case <-blocked.blocked:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("activation did not reach the actual writer")
	}
	err := SetTierContext(t.Context(), h.Name, TierShared, "", "")
	var busy *UserError
	if !errors.As(err, &busy) || busy.Code != "host_busy" {
		cancel()
		t.Fatalf("activation did not retain the actual host lock: %v", err)
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled activation=%v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("cancelled activation did not release owned I/O")
	}
	if source.accepts.Load() != 0 || source.agent.signs.Load() != 0 {
		t.Fatal("a queued activation opened the source before publication")
	}
	if err := SetTierContext(t.Context(), h.Name, TierShared, "", ""); err != nil {
		t.Fatalf("revocation left the policy lock held: %v", err)
	}
	select {
	case <-a.Done():
	default:
		t.Fatal("policy was released before the source capability was revoked")
	}
}

func TestAgentReattachUsesFreshScopeAndRecordsAnotherExposure(t *testing.T) {
	h, scope, source := channelHostFixture(t)
	first := bindChannelFixture(t, h, scope, source, channelTransport{})
	if err := first.Activate(t.Context()); err != nil {
		t.Fatal(err)
	}
	one := signChannel(t, paths.AgentSocket(scope.Session), source.public)
	_ = one.Close()
	first.Close()
	lease, err := agentbridge.NewLease()
	if err != nil {
		t.Fatal(err)
	}
	if lease == scope.Lease {
		t.Fatal("reattach reused its lease")
	}
	scope.Lease = lease
	second := bindChannelFixture(t, h, scope, source, channelTransport{})
	if err := second.Activate(t.Context()); err != nil {
		t.Fatal(err)
	}
	two := signChannel(t, paths.AgentSocket(scope.Session), source.public)
	_ = two.Close()
	grants, err := GrantsForHostContext(t.Context(), h, second.conn.HostID())
	if err != nil || len(grants) != 1 || grants[0].Count != 2 {
		t.Fatalf("reattach history=%+v,%v", grants, err)
	}
	if err := SetTierContext(t.Context(), h.Name, TierThrowaway, "", ""); err != nil {
		t.Fatal(err)
	}
	terminal, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	attached := &attacher{o: AttachOptions{Host: h, Session: scope.Session, Out: terminal}, agent: second}
	if err := attached.watchAgentGrant(t.Context()); err == nil {
		t.Fatal("a lower tier kept the active agent")
	}
	if info, err := terminal.Stat(); err != nil || info.Size() != 0 {
		t.Fatal("revocation wrote terminal bytes")
	}
}

func TestAgentTransportUsesAnOwnConnectionWithoutChangingForwarding(t *testing.T) {
	_, h := sshEnvironmentHost(t, TierOwn)
	s := &SSH{Host: h, ownConnection: true}
	cmd, err := s.Command(t.Context(), "bridge")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cmd.Args, " ")
	for _, required := range []string{"ForwardAgent=no", "ControlMaster=no", "ControlPath=none"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("agent transport lacks %s: %s", required, joined)
		}
	}
	if strings.Contains(joined, "ForwardAgent=yes") || strings.Contains(joined, "ControlMaster=auto") {
		t.Fatal("agent transport enabled forwarding or reused a master")
	}
}

func TestLocalAgentPreflightIsOnlyOneZeroByteHealthConnection(t *testing.T) {
	root := agentClientEnv(t)
	source := newChannelSource(t, root)
	t.Setenv("SSH_AUTH_SOCK", source.path)
	if err := CheckLocalSSHAgentContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	end := time.NewTimer(3 * time.Second)
	defer end.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for source.accepts.Load() != 1 {
		select {
		case <-tick.C:
		case <-end.C:
			t.Fatal("health connection was not observed exactly once")
		}
	}
	if source.agent.lists.Load() != 0 || source.agent.signs.Load() != 0 {
		t.Fatal("preflight performed an agent operation")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := CheckLocalSSHAgentContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled preflight=%v", err)
	}
	if source.accepts.Load() != 1 {
		t.Fatal("cancelled preflight opened another connection")
	}
}
