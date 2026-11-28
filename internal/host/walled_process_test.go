package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

func TestMapAgentMountsClaudeSymlinkAlias(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Replicate Claude Code's native installer layout:
	// ~/.local/bin/claude -> ~/.local/share/claude/versions/2.1.332
	verDir := filepath.Join(home, ".local", "share", "claude", "versions")
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(verDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	realBin := filepath.Join(verDir, "2.1.332")
	if err := os.WriteFile(realBin, []byte("#!/bin/sh\necho 2.1.332\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	linkBin := filepath.Join(binDir, "claude")
	if err := os.Symlink(realBin, linkBin); err != nil {
		t.Fatal(err)
	}

	ag, ok := agents.Find("claude")
	if !ok {
		t.Fatal("claude missing from agents table")
	}
	guestArg0, mounts, pathDirs := mapAgentMounts(linkBin, binDir, ag)
	if guestArg0 != "/opt/agents/bin/claude" {
		t.Fatalf("guestArg0 = %q, want /opt/agents/bin/claude", guestArg0)
	}
	if !slices.Contains(pathDirs, "/opt/agents/bin") {
		t.Fatalf("pathDirs = %v, want /opt/agents/bin", pathDirs)
	}
	var sawInstall, sawAlias bool
	for _, m := range mounts {
		if m.Dest == "/opt/agents/install-0" && m.Source == resolved(t, filepath.Join(home, ".local", "share", "claude")) && m.ReadOnly {
			sawInstall = true
		}
		if m.Dest == "/opt/agents/bin/claude" && m.Source == resolved(t, realBin) && m.ReadOnly {
			sawAlias = true
		}
		if m.Source == home || m.Source == resolved(t, home) || m.Dest == home {
			t.Fatalf("mount leaked host home: %+v", m)
		}
	}
	if !sawInstall || !sawAlias {
		t.Fatalf("mounts = %+v, want install-0 and /opt/agents/bin/claude alias", mounts)
	}
}

func TestMapAgentMountsToolchainAndShebang(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Simulate an npm/nvm CLI script using #!/usr/bin/env node.
	nodeRoot := filepath.Join(home, ".nvm", "versions", "node", "v22")
	nodeBinDir := filepath.Join(nodeRoot, "bin")
	if err := os.MkdirAll(nodeBinDir, 0o700); err != nil {
		t.Fatal(err)
	}
	nodeBin := filepath.Join(nodeBinDir, "node")
	if err := os.WriteFile(nodeBin, []byte("ELF"), 0o755); err != nil {
		t.Fatal(err)
	}
	scriptBin := filepath.Join(nodeBinDir, "codex")
	if err := os.WriteFile(scriptBin, []byte("#!/usr/bin/env node\nconsole.log('ok')\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	guestArg0, mounts, pathDirs := mapAgentMounts(scriptBin, nodeBinDir, agents.Agent{})
	if !strings.HasPrefix(guestArg0, "/opt/agents/tool-") || !strings.HasSuffix(guestArg0, "/bin/codex") {
		t.Fatalf("guestArg0 = %q, want /opt/agents/tool-*/bin/codex", guestArg0)
	}
	if len(mounts) != 1 || mounts[0].Source != resolved(t, nodeRoot) || !mounts[0].ReadOnly {
		t.Fatalf("mounts = %+v, want single RO mount of toolchain root %s", mounts, nodeRoot)
	}
	if len(pathDirs) != 1 || !strings.HasSuffix(pathDirs[0], "/bin") {
		t.Fatalf("pathDirs = %v, want single toolchain bin dir", pathDirs)
	}
}

func TestPrepareProcessWorkspaceRedirectsHomeAndRoot(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))

	for _, raw := range []string{"", "~", tmp, "/"} {
		wsHost, wsGuest, _, _, err := prepareProcessWorkspace(NewArgs{Cwd: raw}, "sess1234")
		if err != nil {
			t.Fatalf("prepareProcessWorkspace(%q): %v", raw, err)
		}
		if wsGuest != "/work/scratch" {
			t.Fatalf("wsGuest(%q) = %q, want /work/scratch", raw, wsGuest)
		}
		if wsHost == tmp || wsHost == "/" {
			t.Fatalf("wsHost(%q) = %q must not be host home or /", raw, wsHost)
		}
	}

	proj := filepath.Join(tmp, "my-project")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	wsHost, wsGuest, _, _, err := prepareProcessWorkspace(NewArgs{Cwd: proj}, "sess1234")
	if err != nil {
		t.Fatalf("prepareProcessWorkspace(proj): %v", err)
	}
	if wsHost != proj || wsGuest != "/work/my-project" {
		t.Fatalf("got (%q, %q), want (%q, /work/my-project)", wsHost, wsGuest, proj)
	}
}

func TestProcessWalledEnvAndProbeFailure(t *testing.T) {
	t.Setenv("SUPER_SECRET_HOST_VAR", "never-leak")
	ag, _ := agents.Find("claude")
	env, term := processWalledEnv(NewArgs{Term: "xterm-256color"}, "sess9999", []string{"/opt/agents/bin"}, ag, false)
	if term != "xterm-256color" {
		t.Fatalf("term = %q", term)
	}
	joined := strings.Join(env, "\n")
	for _, want := range []string{
		"HOME=" + process.GuestHome,
		"USER=agent",
		"LOGNAME=agent",
		"LANG=C.UTF-8",
		"HTTP_PROXY=" + process.GuestProxyURL,
		"HTTPS_PROXY=" + process.GuestProxyURL,
		"NODE_USE_ENV_PROXY=1",
		"PATH=/opt/agents/bin:/usr/local/bin:/usr/bin:/bin",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("env missing %q\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "SUPER_SECRET_HOST_VAR") {
		t.Fatalf("env leaked host variable:\n%s", joined)
	}

	orig := probeProcessWall
	t.Cleanup(func() { probeProcessWall = orig })
	probeProcessWall = func(context.Context) (bool, string, string, string) {
		return false, "", "bubblewrap is missing", "sudo apt install bubblewrap"
	}
	_, err := newProcessWalled(NewArgs{Wall: "process"})
	if err == nil {
		t.Fatal("newProcessWalled succeeded when probe failed")
	}
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != "wall_setup" || pe.Hint == "" {
		t.Fatalf("err = %#v, want proto.Error{Code:wall_setup, Hint:...}", err)
	}
}

// resolved is path with its symlinks resolved, as mapAgentMounts mounts it.
// On macOS the test's temporary folder is under /var, a link to /private/var.
func resolved(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return real
}
