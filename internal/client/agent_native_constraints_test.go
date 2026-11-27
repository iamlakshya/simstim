//go:build darwin || linux

package client

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/iamlakshya/simstim/internal/agentbridge"
)

// These tests exercise the real local OpenSSH agent's constraint decisions.
// Signed synthetic session IDs are protocol fixtures, NOT native KEX or
// collector provenance. TestNativeCollectorRealOpenSSH covers the native hook.
func TestNativeAgentDestinationConstraints(t *testing.T) {
	if os.Getenv("H5_NATIVE_AGENT") != "1" {
		t.Skip("set H5_NATIVE_AGENT=1 in the isolated local native-agent validation job")
	}
	fixture := newNativeConstraintAgent(t)
	user, userPrivate := nativeConstraintKey(t)
	outer, _ := nativeConstraintKey(t)
	destination, _ := nativeConstraintKey(t)
	keyPath := filepath.Join(fixture.root, "generated-user-key")
	block, err := ssh.MarshalPrivateKey(userPrivate, "generated constraint fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	knownHosts := filepath.Join(fixture.root, "known_hosts")
	data := append([]byte("outer.example "), ssh.MarshalAuthorizedKey(outer.PublicKey())...)
	data = append(data, append([]byte("destination.example "), ssh.MarshalAuthorizedKey(destination.PublicKey())...)...)
	if err := os.WriteFile(knownHosts, data, 0o600); err != nil {
		t.Fatal(err)
	}
	outerSID, destinationSID := nativeConstraintSession(t), nativeConstraintSession(t)
	binding := nativeConstraintSyntheticBinding(t, outer, outerSID)
	downstream := nativeConstraintBind(t, destination, destinationSID, false)
	payload := nativeConstraintUserauth(user.PublicKey(), destination.PublicKey(), destinationSID)
	load := func(t *testing.T, destinations ...string) {
		t.Helper()
		fixture.command(t, "-D")
		args := []string{"-q", "-H", knownHosts}
		for _, destination := range destinations {
			args = append(args, "-h", destination)
		}
		fixture.command(t, append(args, keyPath)...)
	}

	t.Run("missing outer baseline permits the destination", func(t *testing.T) {
		load(t, "destination.example")
		connection := fixture.dial(t)
		nativeConstraintExchange(t, connection, downstream, agentFWSuccess)
		nativeConstraintList(t, connection, user.PublicKey(), true)
		nativeConstraintSign(t, connection, user.PublicKey(), payload, true)
	})

	t.Run("mandatory outer prefix denies the same key", func(t *testing.T) {
		load(t, "destination.example")
		connection, trace := fixture.firewall(t, binding)
		nativeConstraintList(t, connection, user.PublicKey(), false)
		nativeConstraintExchange(t, connection, downstream, agentFWSuccess)
		nativeConstraintList(t, connection, user.PublicKey(), false)
		nativeConstraintSign(t, connection, user.PublicKey(), payload, false)
		trace.require(t, binding.wire, agentFWExtension, agentFWRequestIdentities,
			agentFWExtension, agentFWRequestIdentities, agentFWSign)
	})

	t.Run("every allowed hop permits real signing", func(t *testing.T) {
		load(t, "outer.example", "outer.example>destination.example")
		connection, trace := fixture.firewall(t, binding)
		nativeConstraintList(t, connection, user.PublicKey(), true)
		// A forwarding binding cannot itself authorise authentication.
		nativeConstraintSign(t, connection, user.PublicKey(),
			nativeConstraintUserauth(user.PublicKey(), outer.PublicKey(), outerSID), false)
		nativeConstraintExchange(t, connection, downstream, agentFWSuccess)
		nativeConstraintList(t, connection, user.PublicKey(), true)
		nativeConstraintSign(t, connection, user.PublicKey(), payload, true)
		trace.require(t, binding.wire, agentFWExtension, agentFWRequestIdentities,
			agentFWSign, agentFWExtension, agentFWRequestIdentities, agentFWSign)
	})

	t.Run("allowed arrival alone hides further use", func(t *testing.T) {
		load(t, "outer.example")
		connection, trace := fixture.firewall(t, binding)
		nativeConstraintList(t, connection, user.PublicKey(), false)
		nativeConstraintExchange(t, connection, downstream, agentFWSuccess)
		nativeConstraintList(t, connection, user.PublicKey(), false)
		nativeConstraintSign(t, connection, user.PublicKey(), payload, false)
		trace.require(t, binding.wire, agentFWExtension, agentFWRequestIdentities,
			agentFWExtension, agentFWRequestIdentities, agentFWSign)
	})

	t.Run("mutations and providers never reach the real agent", func(t *testing.T) {
		load(t, "outer.example", "outer.example>destination.example")
		connection, trace := fixture.firewall(t, binding)
		nativeConstraintList(t, connection, user.PublicKey(), true)
		// Even a regressed firewall can touch only this disposable agent.
		// Provider requests name a file that does not exist in its private root.
		provider := append(fwString(filepath.Join(fixture.root, "never-provider.so")), fwString("")...)
		generatedAdd := ssh.Marshal(struct {
			Algorithm string
			Public    []byte
			Private   []byte
			Comment   string
		}{ssh.KeyAlgoED25519, userPrivate.Public().(ed25519.PublicKey), userPrivate, "generated only"})
		for _, opcode := range []byte{17, 18, 19, 20, 21, 22, 23, 24, 25, 26} {
			body := provider
			if opcode == 17 {
				body = generatedAdd
			} else if opcode == 19 {
				body = nil // A complete, otherwise valid remove-all request.
			}
			nativeConstraintExchange(t, connection, fwFrame(opcode, body...), agentFWFailure)
		}
		nativeConstraintExchange(t, connection, fwFrame(agentFWExtension, fwString("query")...), agentFWExtensionFailure)
		// A round trip after the denials proves alignment and makes the
		// complete upstream trace observable without a timing assertion.
		nativeConstraintList(t, connection, user.PublicKey(), true)
		trace.require(t, binding.wire, agentFWExtension, agentFWRequestIdentities, agentFWRequestIdentities)
	})

	t.Run("unsupported prefix never consumes a queued request", func(t *testing.T) {
		// This source is deliberately a protocol fixture, not OpenSSH. The
		// preceding cases exercise real successful source-prefix acceptance.
		queued := append(fwFrame(agentFWRequestIdentities), nativeConstraintSignFrame(user.PublicKey(), payload)...)
		remote, _, output, sent, err := fwMemoryRun(binding, queued, fwFrame(agentFWExtensionFailure))
		if !isAgentSourceFailure(err) || remote.reads != 0 || len(output) != 0 || !bytes.Equal(sent, binding.wire) {
			t.Fatalf("unsupported source prefix exposed List or Sign: reads=%d source=%x error=%v", remote.reads, sent, err)
		}
	})
}

func nativeConstraintKey(t *testing.T) (ssh.Signer, ed25519.PrivateKey) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return signer, private
}

func nativeConstraintSession(t *testing.T) []byte {
	t.Helper()
	session := make([]byte, 32)
	if _, err := rand.Read(session); err != nil {
		t.Fatal(err)
	}
	return session
}

func nativeConstraintBind(t *testing.T, host ssh.Signer, session []byte, forwarding bool) []byte {
	t.Helper()
	signature, err := host.Sign(rand.Reader, session)
	if err != nil {
		t.Fatal(err)
	}
	var flag byte
	if forwarding {
		flag = 1
	}
	return fwBindFrame(agentFWSessionBind, string(host.PublicKey().Marshal()), string(session), string(ssh.Marshal(signature)), flag)
}

func nativeConstraintSyntheticBinding(t *testing.T, host ssh.Signer, session []byte) *peerAgentBinding {
	t.Helper()
	// Setting this private bit bypasses provenance only in this fixture.
	// The real agent still verifies the generated host's signature itself.
	return &peerAgentBinding{wire: nativeConstraintBind(t, host, session, true), owner: &Conn{}, verified: true,
		scope: agentbridge.Scope{Session: "abc23def", Lease: "0123456789abcdef0123456789abcdef"}}
}

func nativeConstraintUserauth(user, host ssh.PublicKey, session []byte) []byte {
	// Exact parse_userauth_request order from the pinned ssh-agent.c:
	// session string, SSH_MSG_USERAUTH_REQUEST, user, service, method,
	// sig-follows, public-key algorithm, public key and host-bound host key.
	return ssh.Marshal(struct {
		Session      []byte
		Message      byte
		User         string
		Service      string
		Method       string
		HasSignature bool
		Algorithm    string
		Key          []byte
		HostKey      []byte
	}{session, 50, "fixture", "ssh-connection", "publickey-hostbound-v00@openssh.com", true, user.Type(), user.Marshal(), host.Marshal()})
}

func nativeConstraintExchange(t *testing.T, connection net.Conn, frame []byte, want byte) []byte {
	t.Helper()
	if err := fwSendFrame(connection, frame); err != nil {
		t.Fatal(err)
	}
	reply, err := fwReadFrame(connection)
	if err != nil {
		t.Fatal(err)
	}
	if reply[4] != want || (want != agentFWSignature && len(reply) != 5) {
		t.Fatalf("agent response opcode/length = %d/%d, want %d", reply[4], len(reply), want)
	}
	return reply
}

func nativeConstraintList(t *testing.T, connection net.Conn, key ssh.PublicKey, visible bool) {
	t.Helper()
	keys, err := agent.NewClient(connection).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) > 1 {
		t.Fatalf("disposable agent exposed %d identities, want at most the generated key", len(keys))
	}
	found := len(keys) == 1 && bytes.Equal(keys[0].Blob, key.Marshal())
	if found != visible || (len(keys) == 1 && !found) {
		t.Fatalf("generated constrained identity visibility = %v, want %v", found, visible)
	}
}

func nativeConstraintSign(t *testing.T, connection net.Conn, key ssh.PublicKey, payload []byte, allowed bool) {
	t.Helper()
	want := byte(agentFWFailure)
	if allowed {
		want = agentFWSignature
	}
	reply := nativeConstraintExchange(t, connection, nativeConstraintSignFrame(key, payload), want)
	if !allowed {
		return
	}
	var encoded struct{ Signature []byte }
	var signature ssh.Signature
	if err := ssh.Unmarshal(reply[5:], &encoded); err != nil {
		t.Fatal(err)
	}
	if err := ssh.Unmarshal(encoded.Signature, &signature); err != nil {
		t.Fatal(err)
	}
	if err := key.Verify(payload, &signature); err != nil {
		t.Fatalf("real agent returned an invalid generated-key signature: %v", err)
	}
}

func nativeConstraintSignFrame(key ssh.PublicKey, payload []byte) []byte {
	return fwFrame(agentFWSign, ssh.Marshal(struct {
		Key   []byte
		Data  []byte
		Flags uint32
	}{key.Marshal(), payload, 0})...)
}
