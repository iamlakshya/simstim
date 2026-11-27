//go:build unix

package tui

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/client"
)

// The launcher draws its own screen: a wait of the client for the lock of
// hosts.toml, such as the save of its create, writes no note there, also
// when the command line gave the client a writer for its notes.
func TestTheLauncherWritesNoNotes(t *testing.T) {
	ownFiles(t)
	if err := os.MkdirAll(client.ConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(client.ConfigDir(), "hosts.toml.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	// The wait outlasts the second after which a wait says that it waits.
	defer client.ShortenHostsWait(1500 * time.Millisecond)()
	var notes bytes.Buffer
	m := New(client.WithNotes(context.Background(), &notes), newFakeBackend(), Options{Now: func() time.Time { return now }})
	defer m.Close()
	box := client.Host{Name: "box", SSH: "box"}
	err = client.UpdateHosts(m.ctx, client.HostChange{Kind: "the launcher", Now: &box})
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "hosts_busy" {
		t.Fatalf("the save: %v", err)
	}
	if notes.Len() != 0 {
		t.Errorf("the launcher wrote %q", notes.String())
	}
}
