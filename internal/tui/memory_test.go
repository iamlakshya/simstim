package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

// The writers of lists never touch the session last attached to: it is in
// a file of its own. A launcher.json from before that file keeps its last
// session.
func TestListWritesKeepTheLastSession(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	RememberLast("box", "a1", "web")
	before, err := os.ReadFile(lastPath())
	if err != nil {
		t.Fatal(err)
	}
	RememberSessions(map[string][]client.Session{"box": {{ID: "b2", Name: "api"}}})
	// The launcher writes its lists with the last session it loaded, which
	// may be older than the file's.
	if err := saveMemory(memory{Sessions: map[string][]client.Session{"nuc": nil}, Last: &lastSession{Host: "nuc", ID: "old"}}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(lastPath())
	if err != nil || string(after) != string(before) {
		t.Fatalf("a list write changed the last session: %s, then %s (%v)", before, after, err)
	}
	if host, id, ok := LastSession(); !ok || host != "box" || id != "a1" {
		t.Errorf("LastSession() = %s, %s, %v", host, id, ok)
	}

	// An older launcher.json has the last session in it.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := os.MkdirAll(client.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	old := `{"sessions":{"box":[{"id":"a1","name":"web"}]},"last":{"host":"box","id":"a1","name":"web"}}`
	if err := os.WriteFile(filepath.Join(client.StateDir(), "launcher.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	RememberSessions(map[string][]client.Session{"nuc": {{ID: "c3"}}})
	if host, id, ok := LastSession(); !ok || host != "box" || id != "a1" {
		t.Errorf("after a list write, the older file's last session is %s, %s, %v", host, id, ok)
	}
}
