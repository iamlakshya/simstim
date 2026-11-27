//go:build darwin || linux

package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
)

// Fixture generation and source-agent/load-driver heaps are deliberately
// in the unmeasured parent. Each actor parses its own startup bytes later.
func agentMemoryFixture(t *testing.T, keyShape string) (agentMemoryConfig, string, []string) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "am-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := agentMemoryConfig{Root: root, ClientRoot: filepath.Join(root, "c"), HostRoot: filepath.Join(root, "h"), Binary: binary, Binding: filepath.Join(root, "binding"), Transcript: filepath.Join(root, "transcript"), Source: filepath.Join(root, "source"), Forward: filepath.Join(root, "forward"), ClientControl: filepath.Join(root, "cc"), HostControl: filepath.Join(root, "hc"), NativeControl: filepath.Join(root, "nc"), HostID: strings.Repeat("1", 32)}
	cfg.AgentPath = filepath.Join(cfg.HostRoot, "run", brand.Name, "agent-abc23def.sock")
	cert := agentMemoryMaximumBinding(t, keyShape)
	wire, peer := cert.wire, cert.peer
	cfg.KeyType, cfg.Fingerprint = peer.keyType, peer.fingerprint
	cfg.CAType, cfg.CAFingerprint = cert.caType, cert.caFingerprint
	cfg.CertificateID = cert.keyID
	cfg.Certificate = peer.certificate
	cfg.RequireRefusal, cfg.AllowRefusal, cfg.NativeGrammar = cert.requireRefusal, cert.allowRefusal, cert.nativeGrammar
	hash := sha256.Sum256(wire)
	cfg.BindingHash = hex.EncodeToString(hash[:])
	if err := os.WriteFile(cfg.Binding, wire, 0600); err != nil {
		t.Fatal(err)
	}
	if err := agentMemoryTranscript(cfg.Transcript, cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.AgentPath) > 103 {
		t.Fatal("memory fixture's private Unix path is too long")
	}
	clientEnv, err := agentMemoryEnvironment(cfg.ClientRoot, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "XDG_") {
			t.Setenv(key, "")
		}
	}
	for _, entry := range clientEnv {
		key, value, _ := strings.Cut(entry, "=")
		t.Setenv(key, value)
	}
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	// A native executable, never a script. The shim is a resource contract
	// fixture and is not accepted as a native provenance integration proof.
	if err := os.Link(binary, filepath.Join(bin, "ssh")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SSH_AUTH_SOCK", cfg.Source)
	var hosts []Host
	for i := range 3 {
		hostName := fmt.Sprintf("memory%d", i)
		cfg.HostNames = append(cfg.HostNames, hostName)
		hosts = append(hosts, Host{Name: hostName, SSH: "memory.invalid"})
	}
	if err := SaveHosts(t.Context(), hosts); err != nil {
		t.Fatal(err)
	}
	for i, h := range hosts {
		pinFor(t, h.Name)
		if err := SetTierContext(t.Context(), h.Name, TierOwn, cfg.HostID, ""); err != nil {
			t.Fatal(err)
		}
		writeAgentMemoryMaximumPolicyFixtureAt(t, h, "abc23def", cfg.HostID, 4<<20, 128, []string{"selected", "other", "root"}[i])
	}
	config := filepath.Join(root, "config.json")
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, raw, 0600); err != nil {
		t.Fatal(err)
	}
	env := agentMemoryReplaceEnv(os.Environ(), map[string]string{"AGENT_MODE_MEMORY_ROLE": "client", "AGENT_MODE_MEMORY_CONFIG": config}, false)
	return cfg, config, env
}

func agentMemoryTranscript(path string, cfg agentMemoryConfig) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	start := "debug1: OpenSSH_10.2p1, generated resource fixture\n"
	if cfg.Certificate {
		start += fmt.Sprintf("debug1: Server host certificate: %s %s, serial 1 ID %q CA %s %s valid forever\n", cfg.KeyType, cfg.Fingerprint, cfg.CertificateID, cfg.CAType, cfg.CAFingerprint)
	} else {
		start += "debug1: Server host key: " + cfg.KeyType + " " + cfg.Fingerprint + "\n"
	}
	if err := agentMemoryWrite(file, []byte(start)); err != nil {
		return err
	}
	remaining := (1 << 20) - len(start)
	line := append(bytes.Repeat([]byte("x"), (63<<10)-1), '\n')
	for remaining > 0 {
		k := min(remaining, len(line))
		if k == 1 {
			if err := agentMemoryWrite(file, []byte{'\n'}); err != nil {
				return err
			}
		} else {
			if err := agentMemoryWrite(file, line[:k-1]); err != nil {
				return err
			}
			if err := agentMemoryWrite(file, []byte{'\n'}); err != nil {
				return err
			}
		}
		remaining -= k
	}
	return nil
}

type agentMemorySourceEvent struct {
	phase string
	err   error
}
type agentMemorySource struct {
	listener   *net.UnixListener
	mu         sync.Mutex
	clients    map[net.Conn]bool
	workers    sync.WaitGroup
	done       chan struct{}
	ack, reply chan struct{}
	stop       chan struct{}
	closeOnce  sync.Once
	boundCount int
	events     chan agentMemorySourceEvent
	hash       string
}

func newAgentMemorySource(t *testing.T, cfg agentMemoryConfig) *agentMemorySource {
	t.Helper()
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: cfg.Source, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cfg.Source, 0600); err != nil {
		t.Fatal(err)
	}
	s := &agentMemorySource{listener: l, clients: map[net.Conn]bool{}, done: make(chan struct{}), ack: make(chan struct{}), reply: make(chan struct{}), stop: make(chan struct{}), events: make(chan agentMemorySourceEvent, 128), hash: cfg.BindingHash}
	go func() {
		defer close(s.done)
		for {
			c, err := l.AcceptUnix()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.clients[c] = true
			s.workers.Add(1)
			s.mu.Unlock()
			go func() {
				defer s.workers.Done()
				defer c.Close()
				err := s.serve(c)
				s.mu.Lock()
				delete(s.clients, c)
				s.mu.Unlock()
				if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
					s.emit(agentMemorySourceEvent{err: err})
				}
			}()
		}
	}()
	t.Cleanup(s.Close)
	return s
}

func (s *agentMemorySource) serve(c net.Conn) error {
	var head [5]byte
	if _, err := io.ReadFull(c, head[:4]); err != nil {
		return err
	} // The zero-byte liveness probe ends here.
	n := binary.BigEndian.Uint32(head[:4])
	if n != agentFWMaxBody {
		return errors.New("source did not receive one maximum shared binding")
	}
	h := sha256.New()
	_, _ = h.Write(head[:4])
	var buffer [32 << 10]byte
	if _, err := io.CopyBuffer(h, io.LimitReader(c, int64(n)), buffer[:]); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != s.hash {
		return errors.New("source binding differs between workers")
	}
	s.mu.Lock()
	s.boundCount++
	s.mu.Unlock()
	s.emit(agentMemorySourceEvent{phase: "bound"})
	select {
	case <-s.ack:
	case <-s.stop:
		return net.ErrClosed
	}
	if err := agentMemoryWrite(c, []byte{0, 0, 0, 1, agentFWSuccess}); err != nil {
		return err
	}
	for round := 1; round <= 3; round++ {
		if _, err := io.ReadFull(c, head[:]); err != nil {
			return err
		}
		if binary.BigEndian.Uint32(head[:4]) != agentFWMaxBody || head[4] != agentFWSign {
			return errors.New("source did not receive a maximum sign frame")
		}
		if n, err := io.CopyBuffer(io.Discard, io.LimitReader(c, agentFWMaxBody-1), buffer[:]); err != nil {
			return err
		} else if n != agentFWMaxBody-1 {
			return io.ErrUnexpectedEOF
		}
		s.emit(agentMemorySourceEvent{phase: fmt.Sprintf("sign%d", round)})
		if round == 1 {
			select {
			case <-s.reply:
			case <-s.stop:
				return net.ErrClosed
			}
		}
		s.emit(agentMemorySourceEvent{phase: fmt.Sprintf("reply-start%d", round)})
		if err := agentMemoryPacket(c, agentFWSignature, buffer[:]); err != nil {
			return err
		}
		s.emit(agentMemorySourceEvent{phase: fmt.Sprintf("reply%d", round)})
	}
	_, err := io.CopyBuffer(io.Discard, c, buffer[:])
	return err
}

func agentMemoryPacket(w io.Writer, opcode byte, buffer []byte) error {
	var head [5]byte
	binary.BigEndian.PutUint32(head[:4], agentFWMaxBody)
	head[4] = opcode
	if err := agentMemoryWrite(w, head[:]); err != nil {
		return err
	}
	for i := range buffer {
		buffer[i] = 'x'
	}
	remaining := agentFWMaxBody - 1
	for remaining > 0 {
		k := min(remaining, len(buffer))
		if err := agentMemoryWrite(w, buffer[:k]); err != nil {
			return err
		}
		remaining -= k
	}
	return nil
}

func agentMemoryWaitSource(ctx context.Context, s *agentMemorySource, phase string, want int, pending map[string]int) error {
	for pending[phase] < want {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e := <-s.events:
			if e.err != nil {
				return e.err
			}
			pending[e.phase]++
		}
	}
	return nil
}

func (s *agentMemorySource) Close() {
	s.closeOnce.Do(func() {
		close(s.stop)
		_ = s.listener.Close()
		<-s.done
		s.mu.Lock()
		for c := range s.clients {
			_ = c.Close()
		}
		s.mu.Unlock()
		s.workers.Wait()
	})
}

func (s *agentMemorySource) emit(e agentMemorySourceEvent) {
	select {
	case s.events <- e:
	case <-s.stop:
	}
}
