package guest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/testhome"
)

func namedSSHGuestFixture(t *testing.T) (*Init, *link.Session, string) {
	t.Helper()
	local, remote := net.Pipe()
	root, err := testhome.ShortTempDir("sa")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	g := New(remote, root, slog.New(slog.DiscardHandler))
	supervisor := link.New(local, link.Supervisor, link.Options{})
	run := g.path(host.GuestRunDir())
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.stopSSHAgent(); _ = g.sess.Close(); _ = supervisor.Close() })
	return g, supervisor, filepath.Join(run, "ssh-agent.sock")
}

func TestGuestWithoutNamedKeysHasNoAgentOrDefaultSocketVariable(t *testing.T) {
	g, _, socket := namedSSHGuestFixture(t)
	a := proto.CtlStartArgs{Env: []string{"HOME=/home/agent"}}
	if err := g.memberSSHAgentEnv(&a); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(a.Env, "SSH_AUTH_SOCK="+filepath.Join(host.GuestRunDir(), "ssh-agent.sock")) {
		t.Fatal("closed agent acquired a socket variable")
	}
	if g.sshAgent != nil {
		t.Fatal("an environment path created an agent")
	}
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatal("closed sandbox has an agent endpoint", err)
	}
	explicit := proto.CtlStartArgs{Env: []string{"SSH_AUTH_SOCK=/tmp/user-choice"}}
	if err := g.memberSSHAgentEnv(&explicit); err != nil || len(explicit.Env) != 1 || explicit.Env[0] != "SSH_AUTH_SOCK=/tmp/user-choice" {
		t.Fatal("explicit sandbox environment did not win", err)
	}
}

func TestGuestNamedSSHAgentColdStartRelayAndOwnerCleanup(t *testing.T) {
	g, supervisor, socket := namedSSHGuestFixture(t)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ring := agent.NewKeyring()
	if err := ring.Add(agent.AddedKey{PrivateKey: key, Comment: "DEPLOY_KEY"}); err != nil {
		t.Fatal(err)
	}
	var handlers sync.WaitGroup
	handlers.Add(1)
	go func() {
		defer handlers.Done()
		for {
			stream, err := supervisor.Accept(context.Background())
			if err != nil {
				return
			}
			if stream.Kind() != link.KindAgent {
				_ = stream.Close()
				continue
			}
			handlers.Add(1)
			go func() { defer handlers.Done(); defer stream.Close(); _ = agent.ServeAgent(ring, stream) }()
		}
	}()
	t.Cleanup(func() { g.stopSSHAgent(); _ = supervisor.Close(); handlers.Wait() })
	args := proto.CtlStartArgs{SSHAgent: true}
	if err := g.memberSSHAgentEnv(&args); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(socket)
	if err != nil || st.Mode().Perm() != 0o600 || st.Mode()&os.ModeSocket == 0 {
		t.Fatal("actual private agent endpoint absent", err)
	}
	connection, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
	client := agent.NewClient(connection)
	keys, err := client.List()
	if err != nil || len(keys) != 1 || keys[0].Comment != "DEPLOY_KEY" {
		t.Fatal("guest relay did not reach host key owner", err)
	}
	message := []byte("original authentication bytes")
	signature, err := client.Sign(keys[0], message)
	public, parseErr := ssh.ParsePublicKey(keys[0].Blob)
	if err != nil || parseErr != nil || public.Verify(message, signature) != nil {
		t.Fatal("guest relay changed signing bytes", err, parseErr)
	}
	g.mu.Lock()
	g.ended = true
	g.mu.Unlock()
	g.stopSSHAgent()
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatal("last revoke retained endpoint", err)
	}
	if _, err := client.List(); err == nil {
		t.Fatal("existing agent connection survived revocation")
	}
	if connection, err := net.Dial("unix", socket); err == nil {
		connection.Close()
		t.Fatal("new agent connection survived revocation")
	}
}
