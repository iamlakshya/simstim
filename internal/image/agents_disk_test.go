package image

import (
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const erofsSuperMagic uint32 = 0xE0F5E1E2

func TestBuildAgentsDiskWithMkfsErofs(t *testing.T) {
	mkfsBin, err := exec.LookPath("mkfs.erofs")
	if err != nil {
		t.Skip("mkfs.erofs not installed on this machine")
	}

	root := t.TempDir()
	// Create a secret file outside the install dir and symlink to it inside the install dir.
	secretFile := filepath.Join(root, "outside-secret.txt")
	secretPayload := "SUPER-SECRET-OUTSIDE-CONTENT-MUST-NOT-APPEAR"
	if err := os.WriteFile(secretFile, []byte(secretPayload), 0o600); err != nil {
		t.Fatal(err)
	}

	// Fake install directory with a versioned binary, symlink on PATH, and an escaping symlink.
	installDir := filepath.Join(root, "home", ".local", "share", "claude")
	verDir := filepath.Join(installDir, "versions")
	if err := os.MkdirAll(verDir, 0o755); err != nil {
		t.Fatal(err)
	}
	realBin := filepath.Join(verDir, "claude-1.0")
	if err := os.WriteFile(realBin, []byte("#!/bin/sh\necho claude\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secretFile, filepath.Join(installDir, "escape-link")); err != nil {
		t.Fatal(err)
	}

	pathBinDir := filepath.Join(root, "home", ".local", "bin")
	if err := os.MkdirAll(pathBinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binLink := filepath.Join(pathBinDir, "claude")
	if err := os.Symlink(realBin, binLink); err != nil {
		t.Fatal(err)
	}

	store := NewStore(filepath.Join(root, "store"))
	sources := []AgentSource{{
		Kind:        "claude",
		BinaryName:  "claude",
		BinaryPath:  binLink,
		InstallDirs: []string{installDir},
	}}

	rec1, err := store.BuildAgentsDisk(context.Background(), sources, AgentsDiskOptions{MkfsErofs: mkfsBin})
	if err != nil {
		t.Fatalf("BuildAgentsDisk 1: %v", err)
	}
	if !strings.HasPrefix(rec1.Digest, "sha256:") || rec1.Size == 0 {
		t.Fatalf("unexpected record: %+v", rec1)
	}

	// Verify EROFS superblock magic at byte offset 1024 and mode 0444.
	fi, err := os.Stat(rec1.Path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o444 {
		t.Errorf("mode = %o, want 0444", fi.Mode().Perm())
	}
	raw, err := os.ReadFile(rec1.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 1028 || binary.LittleEndian.Uint32(raw[1024:1028]) != erofsSuperMagic {
		t.Fatalf("output is not a valid EROFS image (magic=%#x)", binary.LittleEndian.Uint32(raw[1024:1028]))
	}
	if strings.Contains(string(raw), secretPayload) {
		t.Fatal("symlink target content outside install dir leaked into agents disk")
	}

	// If dump.erofs is available, verify bin/claude symlink and uid/gid 1000.
	if dumpBin, err := exec.LookPath("dump.erofs"); err == nil {
		out, err := exec.Command(dumpBin, "--path=/bin/claude", rec1.Path).CombinedOutput()
		if err != nil {
			t.Fatalf("dump.erofs /bin/claude failed: %v\n%s", err, out)
		}
		text := string(out)
		if !strings.Contains(text, "Uid: 1000") || !strings.Contains(text, "Gid: 1000") {
			t.Errorf("expected uid/gid 1000 in dump.erofs output:\n%s", text)
		}
	}

	// Determinism: a second build from the same folders gives the exact same digest.
	rec2, err := store.BuildAgentsDisk(context.Background(), sources, AgentsDiskOptions{MkfsErofs: mkfsBin})
	if err != nil {
		t.Fatalf("BuildAgentsDisk 2: %v", err)
	}
	if rec2.Digest != rec1.Digest {
		t.Fatalf("second build changed digest: %s != %s", rec2.Digest, rec1.Digest)
	}

	// Replacing a file with different bytes of the same size and mtime gives a new digest.
	oldMtime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_ = os.Chtimes(realBin, oldMtime, oldMtime)
	recA, err := store.BuildAgentsDisk(context.Background(), sources, AgentsDiskOptions{MkfsErofs: mkfsBin})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(realBin, []byte("#!/bin/sh\necho CLAUDE\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(realBin, oldMtime, oldMtime)
	recB, err := store.BuildAgentsDisk(context.Background(), sources, AgentsDiskOptions{MkfsErofs: mkfsBin})
	if err != nil {
		t.Fatal(err)
	}
	if recB.Digest == recA.Digest {
		t.Fatal("replacing binary bytes kept the same agents disk digest")
	}

	// PruneAgentsDisks keeps latest (recB) and any session-referenced disk (rec1), removes recA.
	keep := map[string]bool{strings.TrimPrefix(rec1.Digest, "sha256:"): true}
	removed, err := store.PruneAgentsDisks(keep)
	if err != nil {
		t.Fatalf("PruneAgentsDisks: %v", err)
	}
	if len(removed) != 1 || removed[0] != recA.Digest {
		t.Fatalf("PruneAgentsDisks removed = %v, want [%s]", removed, recA.Digest)
	}

	// Concurrent BuildAgentsDisk + PruneAgentsDisks never deletes the active latest disk.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 5 {
			if _, err := store.BuildAgentsDisk(context.Background(), sources, AgentsDiskOptions{MkfsErofs: mkfsBin}); err != nil {
				t.Errorf("concurrent BuildAgentsDisk: %v", err)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range 5 {
			if _, err := store.PruneAgentsDisks(nil); err != nil {
				t.Errorf("concurrent PruneAgentsDisks: %v", err)
			}
		}
	}()
	wg.Wait()
	latest, err := store.LatestAgentsDisk()
	if err != nil {
		t.Fatalf("LatestAgentsDisk after concurrent prune: %v", err)
	}
	if _, err := os.Stat(latest.Path); err != nil {
		t.Fatalf("latest agents disk %s missing after concurrent prune: %v", latest.Path, err)
	}
}

func TestBuildAgentsDiskErrorsAndCaps(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "store"))

	t.Run("missing binary", func(t *testing.T) {
		_, err := store.BuildAgentsDisk(context.Background(), []AgentSource{{
			Kind:       "codex",
			BinaryName: "codex",
			BinaryPath: filepath.Join(t.TempDir(), "missing"),
		}}, AgentsDiskOptions{MkfsErofs: "/bin/true"})
		ie, ok := AsError(err)
		if !ok || ie.Code != CodeAgentsDisk || !strings.Contains(ie.Hint, "agents install codex") {
			t.Fatalf("got err=%v, want %s with install hint", err, CodeAgentsDisk)
		}
	})

	t.Run("entry cap", func(t *testing.T) {
		dir := t.TempDir()
		bin := filepath.Join(dir, "tool")
		if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := store.BuildAgentsDisk(context.Background(), []AgentSource{{
			Kind:        "claude",
			BinaryName:  "tool",
			BinaryPath:  bin,
			InstallDirs: []string{dir},
		}}, AgentsDiskOptions{MkfsErofs: "/bin/true", MaxEntries: 1})
		ie, ok := AsError(err)
		if !ok || ie.Code != CodeAgentsDisk {
			t.Fatalf("got err=%v, want %s", err, CodeAgentsDisk)
		}
	})

	t.Run("missing mkfs.erofs", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		_, err := store.BuildAgentsDisk(context.Background(), nil, AgentsDiskOptions{})
		ie, ok := AsError(err)
		if !ok || ie.Code != CodeAgentsDisk || !strings.Contains(ie.Hint, "erofs-utils") {
			t.Fatalf("got err=%v, want %s with erofs-utils hint", err, CodeAgentsDisk)
		}
	})
}
