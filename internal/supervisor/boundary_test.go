package supervisor

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// A supervisor that cannot listen on the session's socket says what to
// try, and its record says that the boot failed.
func TestAListenFailureSaysWhatToTry(t *testing.T) {
	testFolders(t)
	const id = "abcd2345"
	t0 := time.Now().UTC()
	m := &host.Manifest{Schema: 1, ID: id, Name: "box", CreatedAt: t0, Agent: host.AgentInfo{Kind: "claude", Argv: []string{"/bin/sh"}},
		Status: host.Status{State: host.StateStarting, Since: t0}, Wall: &host.WallRecord{Level: "none", Driver: "fake", Version: version.Version, At: t0}}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	// A file where the sockets' folder should be.
	sockets := filepath.Dir(paths.Socket(id))
	if err := paths.EnsurePrivateDir(filepath.Dir(sockets)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sockets, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := sandboxFor
	sandboxFor = func(*host.Manifest) (sandbox, error) {
		return &hostile{disk: fixtureDiskResourcesV1(t), done: make(chan struct{})}, nil
	}
	t.Cleanup(func() { sandboxFor = old })
	err := Run(id)
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != "listen" || pe.Hint == "" {
		t.Fatalf("Run without a sockets' folder = %#v", err)
	}
	if m, _ := host.LoadManifest(id); m.Life == nil || m.Life.End == nil || m.Life.End.Kind != host.EndBoot {
		t.Errorf("the record after a failed listen: %+v", m.Life)
	}
}

// A client whose hello the supervisor cannot take is told what to try.
func TestAnIncompatibleClientIsToldWhatToTry(t *testing.T) {
	id, _, _ := supervised(t)
	conn, err := net.Dial("unix", paths.Socket(id))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	w, r := proto.NewWriter(conn), bufio.NewReader(conn)
	_ = w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: "99.0", Version: version.Version, Role: "bridge"})
	f, err := proto.Read(r)
	var pe proto.Error
	if err != nil || f.Type != proto.TError || json.Unmarshal(f.Payload, &pe) != nil || pe.Hint == "" {
		t.Errorf("an incompatible hello got %v %s, %v", f.Type, f.Payload, err)
	}
}
