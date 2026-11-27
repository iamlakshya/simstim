package supervisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/files"
)

func forkRootFixtureV1(t *testing.T) (*os.Root, string) {
	t.Helper()
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root, directory
}

func TestForkFilesCopiesWorkspaceConversationAndExecutableMode(t *testing.T) {
	work, workPath := forkRootFixtureV1(t)
	home, homePath := forkRootFixtureV1(t)
	destination, destinationPath := forkRootFixtureV1(t)
	if err := os.Mkdir(filepath.Join(workPath, "app"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workPath, "app", "build"), []byte("executable"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(homePath, ".agent"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homePath, ".agent", "conversation"), []byte("turn state"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homePath, "notes.db"), []byte("database"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homePath, "notes.db-wal"), []byte("same turn"), 0600); err != nil {
		t.Fatal(err)
	}
	plan := files.StatePlan{Dirs: []string{".agent"}, Files: []string{"notes.db"}}
	if err := copyForkProcessV1(context.Background(), work, home, destination, plan); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"work/app/build": "executable", "state/.agent/conversation": "turn state"} {
		data, err := os.ReadFile(filepath.Join(destinationPath, name))
		if err != nil || string(data) != want {
			t.Fatalf("%s = %q, %v", name, data, err)
		}
	}
	info, err := os.Stat(filepath.Join(destinationPath, "work/app/build"))
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("executable mode lost: %v %v", info, err)
	}
	current, err := os.ReadFile(filepath.Join(destinationPath, "state-files/CURRENT"))
	if err != nil {
		t.Fatal(err)
	}
	epoch := string(current[:len(current)-1])
	for name, want := range map[string]string{"notes.db": "database", "notes.db-wal": "same turn"} {
		data, err := os.ReadFile(filepath.Join(destinationPath, "state-files/epochs", epoch, name))
		if err != nil || string(data) != want {
			t.Fatalf("%s = %q %v", name, data, err)
		}
	}
	if _, err := os.Stat(filepath.Join(destinationPath, "ring")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a fork copied terminal output")
	}
}

func TestForkTreePreservesSymlinkWithoutReadingItsTarget(t *testing.T) {
	source, sourcePath := forkRootFixtureV1(t)
	destination, destinationPath := forkRootFixtureV1(t)
	outside := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(outside, []byte("outside data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(sourcePath, "link")); err != nil {
		t.Fatal(err)
	}
	if err := copyForkTreeV1(context.Background(), source, destination); err != nil {
		t.Fatal(err)
	}
	link, err := os.Readlink(filepath.Join(destinationPath, "link"))
	if err != nil || link != outside {
		t.Fatalf("symlink changed %q %v", link, err)
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "outside data" {
		t.Fatal("fork touched the symlink target")
	}
}

func TestForkStateDirectorySymlinkAndCancelledFileCannotCopy(t *testing.T) {
	work, _ := forkRootFixtureV1(t)
	home, homePath := forkRootFixtureV1(t)
	destination, _ := forkRootFixtureV1(t)
	if err := os.Symlink(t.TempDir(), filepath.Join(homePath, ".agent")); err != nil {
		t.Fatal(err)
	}
	if err := copyForkProcessV1(context.Background(), work, home, destination, files.StatePlan{Dirs: []string{".agent"}}); err == nil {
		t.Fatal("a state directory symlink was followed")
	}
	source, sourcePath := forkRootFixtureV1(t)
	out, outPath := forkRootFixtureV1(t)
	if err := os.WriteFile(filepath.Join(sourcePath, "data"), []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := copyForkFileV1(ctx, source, out, "data"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled copy returned %v", err)
	}
	if _, err := os.Stat(filepath.Join(outPath, "data")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled copy left an output file")
	}
}
