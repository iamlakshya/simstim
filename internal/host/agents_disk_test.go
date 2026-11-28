package host

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/paths"
)

func TestAttachAgentsDiskRefreshPruneAndDoctorCheck(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(home, "run"))

	// Create fake claude (symlink into ~/.local/share/claude/versions/) and
	// fake codex (symlink into ~/.codex/packages/standalone/...).
	claudeVerDir := filepath.Join(home, ".local", "share", "claude", "versions")
	codexBinDir := filepath.Join(home, ".codex", "packages", "standalone", "releases", "0.172.0", "bin")
	localBin := filepath.Join(home, ".local", "bin")
	for _, d := range []string{claudeVerDir, codexBinDir, localBin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	claudeBin := filepath.Join(claudeVerDir, "2.1.332")
	if err := os.WriteFile(claudeBin, []byte("#!/bin/sh\necho claude-v1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(claudeBin, filepath.Join(localBin, "claude")); err != nil {
		t.Fatal(err)
	}
	codexBin := filepath.Join(codexBinDir, "codex")
	if err := os.WriteFile(codexBin, []byte("#!/bin/sh\necho codex-v1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(codexBin, filepath.Join(localBin, "codex")); err != nil {
		t.Fatal(err)
	}

	// Put localBin on PATH (and keep system PATH so mkfs.erofs or ext4 fallback works).
	t.Setenv("PATH", localBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// Avoid spawning login shell in test by pointing SHELL at /bin/sh with our PATH.
	t.Setenv("SHELL", "/bin/sh")

	// Verify InstalledAgentSourcesFromEnv finds both claude and codex with their InstallDirs.
	sources := InstalledAgentSourcesFromEnv([]string{"PATH=" + localBin}, home)
	var kinds []string
	var codexDirs []string
	for _, s := range sources {
		kinds = append(kinds, s.Kind)
		if s.Kind == "codex" {
			codexDirs = s.InstallDirs
		}
	}
	if !slices.Contains(kinds, "claude") || !slices.Contains(kinds, "codex") {
		t.Fatalf("InstalledAgentSourcesFromEnv kinds = %v, want claude and codex", kinds)
	}
	if !slices.Contains(codexDirs, filepath.Join(home, ".codex", "packages")) {
		t.Fatalf("codex InstallDirs = %v, want ~/.codex/packages", codexDirs)
	}

	ctx := context.Background()
	// First session attaches the initial agents disk and records m1.Agents.
	m1 := &Manifest{Schema: 1, ID: "sess1234", Name: "vm1"}
	if err := paths.EnsurePrivateDir(paths.SessionDir(m1.ID)); err != nil {
		t.Fatal(err)
	}
	disk1Path, err := AttachAgentsDisk(ctx, m1)
	if err != nil {
		t.Fatalf("AttachAgentsDisk 1: %v", err)
	}
	if m1.Agents == "" || !strings.HasPrefix(m1.Agents, "sha256:") {
		t.Fatalf("m1.Agents = %q, want sha256 digest", m1.Agents)
	}
	if err := m1.Save(); err != nil {
		t.Fatal(err)
	}
	disk1Digest := m1.Agents

	// Doctor check now reports the built agents disk.
	checks := AgentsDiskCheck()
	if len(checks) != 1 || !checks[0].OK || checks[0].Name != "agents disk" || !strings.Contains(checks[0].Detail, "claude") || !strings.Contains(checks[0].Detail, "codex") {
		t.Fatalf("AgentsDiskCheck() = %+v", checks)
	}

	// Second EnsureAgentsDisk without changes reuses disk1 without rebuilding.
	recReuse, rebuilt, err := EnsureAgentsDisk(ctx)
	if err != nil || rebuilt || recReuse.Digest != disk1Digest {
		t.Fatalf("EnsureAgentsDisk reuse = (%+v, rebuilt=%v, err=%v), want reuse of %s", recReuse, rebuilt, err, disk1Digest)
	}

	// Simulate `agents install` upgrading claude on the host.
	claudeBinV2 := filepath.Join(claudeVerDir, "2.1.333")
	if err := os.WriteFile(claudeBinV2, []byte("#!/bin/sh\necho claude-v2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(localBin, "claude"))
	if err := os.Symlink(claudeBinV2, filepath.Join(localBin, "claude")); err != nil {
		t.Fatal(err)
	}

	rec2, err := BuildAgentsDisk(ctx)
	if err != nil {
		t.Fatalf("BuildAgentsDisk v2: %v", err)
	}
	if rec2.Digest == disk1Digest {
		t.Fatalf("BuildAgentsDisk after agent upgrade kept old digest %s", disk1Digest)
	}

	// While session m1 still names disk1Digest, PruneImages keeps disk1 AND disk2,
	// and AgentsDiskPath still resolves m1's pinned disk.
	if _, err := PruneImages(); err != nil {
		t.Fatalf("PruneImages while session references disk1: %v", err)
	}
	if got, err := AgentsDiskPath(disk1Digest); err != nil || got != disk1Path {
		t.Fatalf("AgentsDiskPath(disk1) = (%q, %v), want %q", got, err, disk1Path)
	}

	// On m1's next start, AttachAgentsDisk moves m1.Agents to disk2; now PruneImages removes disk1.
	disk2Path, err := AttachAgentsDisk(ctx, m1)
	if err != nil {
		t.Fatalf("AttachAgentsDisk next start: %v", err)
	}
	if m1.Agents != rec2.Digest || disk2Path != rec2.Path {
		t.Fatalf("m1 next start = (%s, %s), want (%s, %s)", m1.Agents, disk2Path, rec2.Digest, rec2.Path)
	}
	if err := m1.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := PruneImages(); err != nil {
		t.Fatalf("PruneImages after session moved to disk2: %v", err)
	}
	if _, err := os.Stat(disk1Path); !os.IsNotExist(err) {
		t.Fatalf("expected unreferenced old agents disk %s to be pruned, stat err=%v", disk1Path, err)
	}
	if _, err := os.Stat(disk2Path); err != nil {
		t.Fatalf("expected latest agents disk %s to remain, err=%v", disk2Path, err)
	}
}

func TestWalledEnvDisablesAgentSelfUpdaters(t *testing.T) {
	claude, ok := agents.Find("claude")
	if !ok {
		t.Fatal("claude not in agent table")
	}
	env, _ := processWalledEnv(NewArgs{}, "sess1", nil, claude, false)
	if !slices.Contains(env, "DISABLE_AUTOUPDATER=1") {
		t.Errorf("claude processWalledEnv missing DISABLE_AUTOUPDATER=1: %v", env)
	}
	plainEnv, _ := walledEnv(NewArgs{}, "sess1", "/home/agent", "/usr/bin/claude", claude, false)
	if !slices.Contains(plainEnv, "DISABLE_AUTOUPDATER=1") {
		t.Errorf("claude walledEnv missing DISABLE_AUTOUPDATER=1: %v", plainEnv)
	}

	codex, ok := agents.Find("codex")
	if !ok {
		t.Fatal("codex not in agent table")
	}
	cenv, _ := processWalledEnv(NewArgs{}, "sess2", nil, codex, false)
	if !slices.Contains(cenv, "NO_UPDATE_NOTIFIER=1") {
		t.Errorf("codex processWalledEnv missing NO_UPDATE_NOTIFIER=1: %v", cenv)
	}
}
