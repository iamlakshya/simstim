package supervisor

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/pullexport"
)

func TestLivePullUsesFreshProtectedCapAndSeedsWithActualRuntimeCwd(t *testing.T) {
	stale, fresh := "8G", "256M"
	live := &host.Manifest{ID: "a2a2a2a2", Cwd: "/work/live", Policy: &api.Policy{LayerValues: api.LayerValues{Sandbox: api.SandboxValues{PullMax: &stale}}}, SeedHeads: map[string]string{"old": strings.Repeat("b", 40)}}
	protected := host.CloneManifest(*live)
	protected.Policy.Sandbox.PullMax = &fresh
	protected.SeedHeads = map[string]string{"fresh": strings.Repeat("a", 40)}
	prepared := false
	call := func(ctx context.Context, op string, args, out any) error {
		if op == proto.CtlBundlePrepare {
			request := args.(proto.CtlBundleArgs)
			if request.Cwd != live.Cwd || len(request.SeedHeads) != 1 || request.SeedHeads[0] != strings.Repeat("a", 40) {
				t.Fatal("live cwd or protected seed identity changed")
			}
			*out.(*proto.CtlBundleReady) = proto.CtlBundleReady{ID: request.ID, Empty: true}
			prepared = true
		}
		return nil
	}
	source, err := guestPullSource(live, &protected, call,
		func(context.Context, link.Kind, any) (*link.Stream, error) {
			t.Fatal("empty capture opened stream")
			return nil, nil
		},
		func(context.Context, []string) error { return nil }, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if source.PullMax() != 256<<20 {
		t.Fatal("stale live policy bypassed the fresh cap under the retained lease")
	}
	if _, err := source.Capture(context.Background(), pullexport.CaptureArgs{}, io.Discard); err != nil || !prepared {
		t.Fatal("the real guest source did not prepare", err)
	}
}
