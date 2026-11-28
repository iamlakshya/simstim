package image

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const ext4SuperMagic uint16 = 0xEF53

func TestEnsureAgentsDiskExt4FallbackReuseRefreshAndPrune(t *testing.T) {
	// Force pure-Go hcsshim tar2ext4 fallback by isolating PATH so mkfs.erofs is absent.
	t.Setenv("PATH", t.TempDir())

	root := t.TempDir()
	// Create a secret file outside the install dir and a symlink pointing to it.
	secretFile := filepath.Join(root, "outside-secret.txt")
	secretPayload := "EXT4-OUTSIDE-SECRET-MUST-NOT-LEAK"
	if err := os.WriteFile(secretFile, []byte(secretPayload), 0o600); err != nil {
		t.Fatal(err)
	}
	installDir := filepath.Join(root, "home", ".codex", "packages", "standalone", "releases", "0.172.0", "bin")
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secretFile, filepath.Join(root, "home", ".codex", "packages", "escape-link")); err != nil {
		t.Fatal(err)
	}
	realBin := filepath.Join(installDir, "codex")
	fixedTime := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if err := os.WriteFile(realBin, []byte("#!/bin/sh\necho codex-v1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(realBin, fixedTime, fixedTime); err != nil {
		t.Fatal(err)
	}
	helperBin := filepath.Join(installDir, "codex-exec-server")
	if err := os.WriteFile(helperBin, []byte("#!/bin/sh\necho helper\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(helperBin, fixedTime, fixedTime); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(root, "home", ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binLink := filepath.Join(binDir, "codex")
	if err := os.Symlink(realBin, binLink); err != nil {
		t.Fatal(err)
	}

	sources := []AgentSource{{
		Kind:        "codex",
		BinaryName:  "codex",
		BinaryPath:  binLink,
		InstallDirs: []string{filepath.Join(root, "home", ".codex", "packages")},
	}}

	store := NewStore(filepath.Join(root, "store"))
	// Use default MaxBytes (4 GiB) to confirm tar2ext4 builds a compact image, not a 4 GiB file.
	opts := AgentsDiskOptions{
		AllowExt4Fallback: true,
	}

	rec1, rebuilt1, err := store.EnsureAgentsDisk(context.Background(), sources, opts)
	if err != nil {
		t.Fatalf("EnsureAgentsDisk 1: %v", err)
	}
	if !rebuilt1 || rec1.Format != AgentsFormatExt4 || !strings.HasSuffix(rec1.Path, ".img") || rec1.SourceDigest == "" {
		t.Fatalf("unexpected ext4 agents disk record: %+v (rebuilt=%v)", rec1, rebuilt1)
	}
	if rec1.Size <= 0 || rec1.Size > 64<<20 {
		t.Fatalf("ext4 agents disk size = %d bytes, want compact image (< 64 MiB)", rec1.Size)
	}
	fi, err := os.Stat(rec1.Path)
	if err != nil || fi.Mode().Perm() != 0o444 {
		t.Fatalf("agents disk stat = %v, err=%v, want mode 0444", fi, err)
	}
	// Verify ext4 superblock magic (0xEF53 at offset 1024+0x38 = 1080) and no symlink escape leak.
	raw, err := os.ReadFile(rec1.Path)
	if err != nil {
		t.Fatalf("read ext4 image: %v", err)
	}
	if len(raw) < 1082 {
		t.Fatalf("ext4 image too short: %d bytes", len(raw))
	}
	if got := binary.LittleEndian.Uint16(raw[1080:1082]); got != ext4SuperMagic {
		t.Fatalf("ext4 superblock magic = %#x, want %#x", got, ext4SuperMagic)
	}
	if strings.Contains(string(raw), secretPayload) {
		t.Fatal("symlink target content outside install dir leaked into ext4 agents disk")
	}

	// Second EnsureAgentsDisk with unchanged files reuses without rebuilding.
	rec2, rebuilt2, err := store.EnsureAgentsDisk(context.Background(), sources, opts)
	if err != nil {
		t.Fatalf("EnsureAgentsDisk 2: %v", err)
	}
	if rebuilt2 || rec2.Digest != rec1.Digest || rec2.Path != rec1.Path {
		t.Fatalf("expected cache reuse, got rec=%+v rebuilt=%v (want %s)", rec2, rebuilt2, rec1.Digest)
	}

	// Explicit BuildAgentsDisk on the same inputs gives the exact same digest (deterministic).
	recDet, err := store.BuildAgentsDisk(context.Background(), sources, opts)
	if err != nil {
		t.Fatalf("BuildAgentsDisk deterministic check: %v", err)
	}
	if recDet.Digest != rec1.Digest {
		t.Fatalf("deterministic ext4 build digest changed: %s != %s", recDet.Digest, rec1.Digest)
	}

	// Replace the binary via atomic rename with DIFFERENT bytes of the EXACT SAME size and mtime.
	// Inode/ctime changes, so EnsureAgentsDisk must detect the change and build a new disk.
	tmpNew := filepath.Join(installDir, ".codex.new")
	if err := os.WriteFile(tmpNew, []byte("#!/bin/sh\necho codex-v2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(tmpNew, fixedTime, fixedTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmpNew, realBin); err != nil {
		t.Fatal(err)
	}
	rec3, rebuilt3, err := store.EnsureAgentsDisk(context.Background(), sources, opts)
	if err != nil {
		t.Fatalf("EnsureAgentsDisk after same-size same-mtime replacement: %v", err)
	}
	if !rebuilt3 || rec3.Digest == rec1.Digest || rec3.SourceDigest == rec1.SourceDigest {
		t.Fatalf("expected rebuild on replaced binary, got rec3=%+v rebuilt=%v (old=%+v)", rec3, rebuilt3, rec1)
	}

	// ResolveAgentsDisk finds both stored .img disks and rejects invalid digests.
	if gotPath, err := store.ResolveAgentsDisk(rec1.Digest); err != nil || gotPath != rec1.Path {
		t.Fatalf("ResolveAgentsDisk(rec1) = (%q, %v), want %q", gotPath, err, rec1.Path)
	}
	if gotPath, err := store.ResolveAgentsDisk(rec3.Digest); err != nil || gotPath != rec3.Path {
		t.Fatalf("ResolveAgentsDisk(rec3) = (%q, %v), want %q", gotPath, err, rec3.Path)
	}
	if _, err := store.ResolveAgentsDisk("../escape"); err == nil {
		t.Fatal("ResolveAgentsDisk accepted path traversal")
	}

	// PruneAgentsDisks protects pinned session disk rec1 while keeping latest rec3.
	keep := map[string]bool{strings.TrimPrefix(rec1.Digest, "sha256:"): true}
	removed, err := store.PruneAgentsDisks(keep)
	if err != nil || len(removed) != 0 {
		t.Fatalf("PruneAgentsDisks(keep rec1) = (%v, %v), want none removed", removed, err)
	}
	// Once no session references rec1, PruneAgentsDisks removes rec1 and keeps rec3.
	removed, err = store.PruneAgentsDisks(nil)
	if err != nil || len(removed) != 1 || removed[0] != rec1.Digest {
		t.Fatalf("PruneAgentsDisks(nil) = (%v, %v), want [%s]", removed, err, rec1.Digest)
	}
	if _, err := os.Stat(rec3.Path); err != nil {
		t.Fatalf("latest disk rec3 missing after prune: %v", err)
	}
}
