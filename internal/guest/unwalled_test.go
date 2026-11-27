package guest

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

func unwalledGuestFixtureV1(t *testing.T) *Init {
	t.Helper()
	parent := ""
	if runtime.GOOS != "windows" {
		parent = "/tmp"
	}
	base, err := os.MkdirTemp(parent, "guest-unwalled-")
	if err != nil {
		t.Fatal(err)
	}
	if real, err := filepath.EvalSymlinks(base); err == nil {
		base = real
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := cachefs.CaptureRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return &Init{unwalled: &unwalledGuestV1{id: "a2a2a2a2", home: base, runtime: root, ref: ref}, lifetime: context.Background()}
}

func TestUnwalledGuestKeepsHostFoldersAndRefusesSandboxControls(t *testing.T) {
	g := unwalledGuestFixtureV1(t)
	home := g.unwalled.home
	args := proto.CtlStartArgs{Session: g.unwalled.id, Argv: []string{"/bin/sh", "-l"}, Cwd: home, Env: []string{"HOME=" + home, "PATH=/usr/bin:/bin", "HTTP_PROXY=http://user.example:3128"}}
	if err := g.projectMember(&args, true); err != nil {
		t.Fatal(err)
	}
	if g.path(host.GuestHome) != home || g.mapValue(home) != home || g.path("/tmp") != "/tmp" {
		t.Fatal("the none guest rebased a real host path")
	}
	if !slices.Contains(args.Env, "HTTP_PROXY=http://user.example:3128") {
		t.Fatal("the none guest rewrote the user's environment as a sandbox proxy")
	}
	if args.DNS != "" || g.proxy != nil || g.dns != nil || g.decoysApplied || g.packageSettingsRequired {
		t.Fatal("none enabled a sandbox door")
	}
	if g.ptyABI() != proto.GuestNonePTYV1 {
		t.Fatal("none claimed another guest's account ABI")
	}
	if got := g.memberFilesV1(args.Session).Sock; filepath.Dir(got) != g.unwalled.runtime.Name() {
		t.Fatal("none placed its holder socket outside its runtime folder")
	}
	args.Proxy = "http://127.0.0.1:3128"
	if err := g.projectMember(&args, true); err == nil {
		t.Fatal("none accepted a sandbox network projection")
	}
}

func TestUnwalledGuestCannotReceiveBoundSentinels(t *testing.T) {
	g := unwalledGuestFixtureV1(t)
	args := proto.CtlStartArgs{Session: g.unwalled.id, Cwd: g.unwalled.home, Argv: []string{"/bin/sh"}, Secrets: map[string]string{"TOKEN": "stand-in"}, SecretModes: map[string]api.SecretMode{"TOKEN": api.SecretModeBound}}
	if err := g.projectMember(&args, true); err == nil {
		t.Fatal("none accepted a bound credential as an exposed value")
	}
}
