package supervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func namedSSHFixture(t *testing.T) (api.Secret, ssh.Signer) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return api.Secret(pem.EncodeToMemory(block)), signer
}

func namedSSHClient(t *testing.T, owner *namedSSHAgent) (agent.ExtendedAgent, net.Conn) {
	t.Helper()
	local, remote := net.Pipe()
	deadline := time.Now().Add(5 * time.Second)
	_ = local.SetDeadline(deadline)
	_ = remote.SetDeadline(deadline)
	done := make(chan struct{})
	go func() { defer close(done); _ = serveNamedSSHProtocol(&namedSSHConnection{owner: owner}, remote) }()
	t.Cleanup(func() { _ = local.Close(); _ = remote.Close(); <-done })
	return agent.NewClient(local), local
}

func TestNamedSSHAgentListsOnlyGrantedKeysAndAuditsSignatures(t *testing.T) {
	value, signer := namedSSHFixture(t)
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	audit := openAudit(path, slog.New(slog.DiscardHandler))
	t.Cleanup(audit.close)
	owner, err := newNamedSSHAgent(t.Context(), []string{"DEPLOY_KEY"}, map[string]api.Secret{"DEPLOY_KEY": value}, audit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.close)
	client, _ := namedSSHClient(t, owner)
	keys, err := client.List()
	if err != nil || len(keys) != 1 || keys[0].Comment != "DEPLOY_KEY" || string(keys[0].Blob) != string(signer.PublicKey().Marshal()) {
		t.Fatal("agent listed a different key set", err)
	}
	data := []byte("an SSH authentication message")
	signature, err := client.Sign(keys[0], data)
	if err != nil || signer.PublicKey().Verify(data, signature) != nil {
		t.Fatal("named key did not sign the original bytes", err)
	}
	_, other := namedSSHFixture(t)
	if _, err := client.Sign(other.PublicKey(), data); err == nil {
		t.Fatal("unlisted key signed")
	}
	lines, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(lines), string(value)) || strings.Contains(string(lines), string(data)) {
		t.Fatal("key material or signed data entered the audit")
	}
	var entry auditLine
	if json.Unmarshal(lines, &entry) != nil || entry.Event != "ssh_sign" || entry.Fields["fingerprint"] != ssh.FingerprintSHA256(signer.PublicKey()) {
		t.Fatal("signature fingerprint audit missing")
	}
	if _, exists := entry.Fields["server_host_key"]; exists {
		t.Fatal("unbound signature invented a server host key")
	}
}

func TestNamedSSHAgentRefusesAllMutatingProtocolOperations(t *testing.T) {
	value, _ := namedSSHFixture(t)
	owner, err := newNamedSSHAgent(t.Context(), []string{"DEPLOY_KEY"}, map[string]api.Secret{"DEPLOY_KEY": value}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.close)
	client, raw := namedSSHClient(t, owner)
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Add(agent.AddedKey{PrivateKey: private}); err == nil {
		t.Fatal("added an ungranted private key")
	}
	listed, err := client.List()
	if err != nil || len(listed) != 1 {
		t.Fatal(err)
	}
	if client.Remove(listed[0]) == nil || client.RemoveAll() == nil || client.Lock([]byte("lock")) == nil || client.Unlock([]byte("lock")) == nil {
		t.Fatal("a key mutation or lock succeeded")
	}
	for _, opcode := range []byte{1, 7, 8, 9, 10, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 255} {
		if _, err := raw.Write([]byte{0, 0, 0, 1, opcode}); err != nil {
			t.Fatal(err)
		}
		var response [5]byte
		if _, err := io.ReadFull(raw, response[:]); err != nil || response != [5]byte{0, 0, 0, 1, 5} {
			t.Fatalf("opcode %d was not refused: %v", opcode, err)
		}
	}
	if _, err := client.Extension("unrecognised@example.com", nil); err == nil {
		t.Fatal("unknown extension succeeded")
	}
	listed, err = client.List()
	if err != nil || len(listed) != 1 {
		t.Fatal("rejected mutations changed named identities", err)
	}
}

func namedSSHBind(t *testing.T, host ssh.Signer, id []byte, forwarding byte) []byte {
	t.Helper()
	signature, err := host.Sign(rand.Reader, id)
	if err != nil {
		t.Fatal(err)
	}
	var out []byte
	for _, field := range [][]byte{host.PublicKey().Marshal(), id, ssh.Marshal(signature)} {
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(field)))
		out = append(out, size[:]...)
		out = append(out, field...)
	}
	return append(out, forwarding)
}

func TestNamedSSHBindingVerifiesSignatureAndStaysConnectionLocal(t *testing.T) {
	value, signer := namedSSHFixture(t)
	_, host := namedSSHFixture(t)
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	audit := openAudit(path, slog.New(slog.DiscardHandler))
	t.Cleanup(audit.close)
	owner, err := newNamedSSHAgent(t.Context(), []string{"DEPLOY_KEY"}, map[string]api.Secret{"DEPLOY_KEY": value}, audit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.close)
	client, _ := namedSSHClient(t, owner)
	binding := namedSSHBind(t, host, []byte("connection-session"), 0)
	if _, err := client.Extension("session-bind@openssh.com", binding); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Extension("session-bind@openssh.com", binding); err != nil {
		t.Fatal("identical binding was not idempotent", err)
	}
	if _, err := client.Sign(signer.PublicKey(), []byte("first")); err != nil {
		t.Fatal(err)
	}
	other, _ := namedSSHClient(t, owner)
	if _, err := other.Sign(signer.PublicKey(), []byte("second")); err != nil {
		t.Fatal(err)
	}
	lines, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rows := strings.Split(strings.TrimSpace(string(lines)), "\n")
	if len(rows) != 2 {
		t.Fatal("wrong signature audit count")
	}
	var first, second auditLine
	if json.Unmarshal([]byte(rows[0]), &first) != nil || json.Unmarshal([]byte(rows[1]), &second) != nil {
		t.Fatal("invalid audit JSON")
	}
	if first.Fields["server_host_key"] != ssh.FingerprintSHA256(host.PublicKey()) {
		t.Fatal("verified server binding missing")
	}
	if _, exists := second.Fields["server_host_key"]; exists {
		t.Fatal("server binding crossed agent connections")
	}
	for _, invalid := range [][]byte{binding[:len(binding)-1], append(append([]byte(nil), binding...), 0), namedSSHBind(t, host, []byte("other"), 2)} {
		if _, err := other.Extension("session-bind@openssh.com", invalid); err == nil {
			t.Fatal("malformed binding succeeded")
		}
	}
	corrupt := append([]byte(nil), binding...)
	corrupt[len(corrupt)-2] ^= 1
	if _, err := other.Extension("session-bind@openssh.com", corrupt); err == nil {
		t.Fatal("invalid host signature succeeded")
	}
}

func TestNamedSSHAgentRefusesAfterOriginalOwnerEnds(t *testing.T) {
	value, signer := namedSSHFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	owner, err := newNamedSSHAgent(ctx, []string{"DEPLOY_KEY"}, map[string]api.Secret{"DEPLOY_KEY": value}, nil)
	if err != nil {
		t.Fatal(err)
	}
	connection := &namedSSHConnection{owner: owner}
	cancel()
	if _, err := connection.List(); err == nil {
		t.Fatal("ended owner listed keys")
	}
	if _, err := connection.Sign(signer.PublicKey(), []byte("message")); err == nil {
		t.Fatal("ended owner signed")
	}
	owner.close()
	if len(owner.keys) != 0 || len(owner.order) != 0 {
		t.Fatal("closed owner retained signer references")
	}
}

func TestNamedSSHAgentRejectsOversizedPacketBeforeReadingBody(t *testing.T) {
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], namedSSHMaxPacket+1)
	stream := &namedSSHPacket{Reader: strings.NewReader(string(header[:])), Writer: io.Discard}
	if err := serveNamedSSHProtocol(nil, stream); err != errNamedSSHRefused {
		t.Fatal("oversized request was not refused at its header", err)
	}
}

func TestNamedSSHGrantDiagnosticsNeverContainPrivateInput(t *testing.T) {
	marker := api.Secret("private-key-command-output-that-is-not-a-key")
	_, err := newNamedSSHAgent(t.Context(), []string{"DEPLOY_KEY"}, map[string]api.Secret{"DEPLOY_KEY": marker}, nil)
	var refusal *proto.Error
	if err == nil || strings.Contains(err.Error(), string(marker)) || !errors.As(err, &refusal) || refusal.Hint == "" {
		t.Fatal("invalid key did not produce a private-safe actionable error")
	}
	grant := api.NamedSSHKeysV1{Version: 1, Names: []string{"DEPLOY_KEY"}, Values: map[string]api.Secret{"DEPLOY_KEY": marker}}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		if strings.Contains(fmt.Sprintf(verb, grant), string(marker)) {
			t.Fatal("private grant formatting leaked a value")
		}
	}
}
