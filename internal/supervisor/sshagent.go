package supervisor

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

const namedSSHMaxKey = 64 << 10
const namedSSHConnections = 16

var errNamedSSHRefused = errors.New("the named SSH agent refused this request")

type namedSSHKey struct {
	name   string
	signer ssh.Signer
}

// A namedSSHAgent owns only the keys supplied by the original grant carrier.
// Neither a guest enable bit nor a saved policy can construct those values.
type namedSSHAgent struct {
	mu     sync.Mutex
	ctx    context.Context
	keys   map[string]namedSSHKey
	order  []string
	active map[*link.Stream]struct{}
	audit  *auditLog
	closed bool
	wg     sync.WaitGroup
}

func newNamedSSHAgent(ctx context.Context, names []string, values map[string]api.Secret, audit *auditLog) (*namedSSHAgent, error) {
	if ctx == nil || ctx.Err() != nil || len(names) == 0 || len(names) != len(values) {
		return nil, namedSSHError("the named SSH key grant is incomplete")
	}
	a := &namedSSHAgent{ctx: ctx, keys: make(map[string]namedSSHKey), active: make(map[*link.Stream]struct{}), audit: audit}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if api.CheckSecretName(name, false) != api.SecretNameOK || seen[name] {
			return nil, namedSSHError("the named SSH key grant has an invalid or repeated name")
		}
		seen[name] = true
		value, ok := values[name]
		if !ok || len(value) == 0 || len(value) > namedSSHMaxKey {
			return nil, namedSSHError("a named SSH key is missing or exceeds its bound")
		}
		// The client command prints an unencrypted key. A parser error is never
		// returned to a log, because it may contain text from that command.
		signer, err := ssh.ParsePrivateKey([]byte(value))
		if err != nil {
			return nil, namedSSHError("a named SSH key cannot be parsed")
		}
		blob := string(signer.PublicKey().Marshal())
		if _, exists := a.keys[blob]; exists {
			return nil, namedSSHError("two named SSH keys have the same public key")
		}
		a.keys[blob] = namedSSHKey{name: name, signer: signer}
		a.order = append(a.order, blob)
	}
	slices.SortFunc(a.order, func(left, right string) int {
		if a.keys[left].name < a.keys[right].name {
			return -1
		}
		if a.keys[left].name > a.keys[right].name {
			return 1
		}
		return 0
	})
	return a, nil
}

func namedSSHError(message string) error {
	return &proto.Error{Code: api.CodeNamedSSHKeyRefused, Message: message,
		Hint: "Check the named SSH key secret and its host grant."}
}

// close excludes new requests, releases every active stream, and joins all
// handlers before dropping the last owned signer references.
func (a *namedSSHAgent) close() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.closed = true
	streams := make([]*link.Stream, 0, len(a.active))
	for st := range a.active {
		streams = append(streams, st)
	}
	clear(a.keys)
	a.order = nil
	a.mu.Unlock()
	for _, st := range streams {
		_ = st.SetDeadline(immediateSSHDeadline())
		_ = st.Close()
	}
	a.wg.Wait()
}

func (a *namedSSHAgent) admit(st *link.Stream) bool {
	if a == nil {
		_ = st.Close()
		return false
	}
	a.mu.Lock()
	if a.closed || a.ctx.Err() != nil || len(a.active) >= namedSSHConnections {
		a.mu.Unlock()
		_ = st.Close()
		return false
	}
	a.active[st] = struct{}{}
	a.wg.Add(1)
	a.mu.Unlock()
	return true
}

func (a *namedSSHAgent) serve(st *link.Stream) {
	defer a.wg.Done()
	defer func() {
		a.mu.Lock()
		delete(a.active, st)
		a.mu.Unlock()
		_ = st.Close()
	}()
	stop := context.AfterFunc(a.ctx, func() { _ = st.SetDeadline(immediateSSHDeadline()); _ = st.Close() })
	defer stop()
	_ = serveNamedSSHProtocol(&namedSSHConnection{owner: a}, st)
}

// A binding belongs to this one agent connection. It describes the server
// whose host key signed its session ID. It grants no network permission.
type namedSSHConnection struct {
	owner    *namedSSHAgent
	bindings []namedSSHBinding
}

func (c *namedSSHConnection) List() ([]*agent.Key, error) {
	a := c.owner
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.ctx.Err() != nil {
		return nil, errNamedSSHRefused
	}
	keys := make([]*agent.Key, 0, len(a.order))
	for _, blob := range a.order {
		key := a.keys[blob]
		keys = append(keys, &agent.Key{Format: key.signer.PublicKey().Type(), Blob: []byte(blob), Comment: key.name})
	}
	return keys, nil
}

func (c *namedSSHConnection) Sign(key ssh.PublicKey, data []byte) (*ssh.Signature, error) {
	return c.SignWithFlags(key, data, 0)
}

func (c *namedSSHConnection) SignWithFlags(key ssh.PublicKey, data []byte, flags agent.SignatureFlags) (*ssh.Signature, error) {
	if key == nil || len(data) > namedSSHMaxPacket {
		return nil, errNamedSSHRefused
	}
	a := c.owner
	a.mu.Lock()
	defer a.mu.Unlock()
	selected, ok := a.keys[string(key.Marshal())]
	if !ok || a.closed || a.ctx.Err() != nil {
		return nil, errNamedSSHRefused
	}
	var signature *ssh.Signature
	var err error
	if flags == 0 {
		signature, err = selected.signer.Sign(rand.Reader, data)
	} else {
		algorithm := ""
		switch flags {
		case agent.SignatureFlagRsaSha256:
			algorithm = ssh.KeyAlgoRSASHA256
		case agent.SignatureFlagRsaSha512:
			algorithm = ssh.KeyAlgoRSASHA512
		}
		rsaSigner, supported := selected.signer.(ssh.AlgorithmSigner)
		if algorithm == "" || selected.signer.PublicKey().Type() != ssh.KeyAlgoRSA || !supported {
			return nil, errNamedSSHRefused
		}
		signature, err = rsaSigner.SignWithAlgorithm(rand.Reader, data, algorithm)
	}
	if err != nil {
		return nil, errNamedSSHRefused
	}
	fields := map[string]any{"fingerprint": ssh.FingerprintSHA256(selected.signer.PublicKey())}
	if len(c.bindings) > 0 {
		fields["server_host_key"] = c.bindings[len(c.bindings)-1].fingerprint
	}
	a.audit.write("ssh_sign", fields)
	return signature, nil
}

func (*namedSSHConnection) Add(agent.AddedKey) error       { return errNamedSSHRefused }
func (*namedSSHConnection) Remove(ssh.PublicKey) error     { return errNamedSSHRefused }
func (*namedSSHConnection) RemoveAll() error               { return errNamedSSHRefused }
func (*namedSSHConnection) Lock([]byte) error              { return errNamedSSHRefused }
func (*namedSSHConnection) Unlock([]byte) error            { return errNamedSSHRefused }
func (*namedSSHConnection) Signers() ([]ssh.Signer, error) { return nil, errNamedSSHRefused }

var _ agent.ExtendedAgent = (*namedSSHConnection)(nil)

func immediateSSHDeadline() time.Time { return time.Now() }

func (*namedSSHAgent) Format(f fmt.State, _ rune)      { _, _ = io.WriteString(f, api.Redacted) }
func (namedSSHKey) Format(f fmt.State, _ rune)         { _, _ = io.WriteString(f, api.Redacted) }
func (*namedSSHConnection) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }
