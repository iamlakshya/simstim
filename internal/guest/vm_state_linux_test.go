//go:build linux

package guest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/files"
)

func TestVMCaptureIsPendingAndCannotReplaceHostAcceptedGeneration(t *testing.T) {
	session, home := t.TempDir(), t.TempDir()
	prior := strings.Repeat("a", 32)
	current := strings.Repeat("b", 32)
	old := filepath.Join(session, "state/files/generations", prior)
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, ".claude.json"), []byte("accepted"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{".claude.json": "pending", ".claude.json-wal": "journal", ".bashrc": "ephemeral"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	state := &vmState{session: session, home: home, current: current, plan: files.StatePlan{Files: []string{".claude.json"}}}
	if err := state.capture(false); err != nil || state.ready {
		t.Fatal("unclean stop became ready", err)
	}
	if _, err := os.Stat(filepath.Join(session, "state/files/generations", current)); !os.IsNotExist(err) {
		t.Fatal("unclean stop wrote pending data")
	}
	if err := state.capture(true); err != nil || !state.ready {
		t.Fatal(err)
	}
	for name, want := range map[string]string{".claude.json": "pending", ".claude.json-wal": "journal"} {
		data, err := os.ReadFile(filepath.Join(session, "state/files/generations", current, name))
		if err != nil || string(data) != want {
			t.Fatal("family lost", name, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(old, ".claude.json"))
	if err != nil || string(data) != "accepted" {
		t.Fatal("guest replaced host accepted generation", err)
	}
	if _, err := os.Stat(filepath.Join(session, "state/files/generations", current, ".bashrc")); !os.IsNotExist(err) {
		t.Fatal("arbitrary home file persisted")
	}
	if _, err := os.Stat(filepath.Join(session, "CURRENT")); !os.IsNotExist(err) {
		t.Fatal("guest selected its own committed epoch")
	}
	if err := state.capture(true); err == nil {
		t.Fatal("pending generation was reused")
	}
}

func TestVMCaptureFailureRemovesOnlyItsOwnIncompletePendingDirectory(t *testing.T) {
	session, home := t.TempDir(), t.TempDir()
	nonce := strings.Repeat("c", 32)
	if err := os.MkdirAll(filepath.Join(session, "state/files/generations"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(outside, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, ".claude.json")); err != nil {
		t.Fatal(err)
	}
	state := &vmState{session: session, home: home, current: nonce, plan: files.StatePlan{Files: []string{".claude.json"}}}
	if state.capture(true) == nil || state.ready {
		t.Fatal("symlink family accepted")
	}
	if _, err := os.Stat(filepath.Join(session, "state/files/generations", nonce)); !os.IsNotExist(err) {
		t.Fatal("failed pending generation survived")
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "unchanged" {
		t.Fatal("capture followed symlink", err)
	}
}
