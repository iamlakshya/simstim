//go:build !windows

// These tests run the real host side, which runs on Linux and macOS only.

package bundle_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/bundle"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// The hint of a bundle that stopped, or whose connection closed, names the
// session by the ID that the bundle found (cli-spec 9.2). A bundle that
// stopped before it found the session lists the host's sessions instead.
func TestTheBundlesHintsNameTheSessionItFound(t *testing.T) {
	h := hosttest.New(t)
	ch, tr := h.ClientHost("box")
	if err := client.SaveHosts([]client.Host{ch}); err != nil {
		t.Fatal(err)
	}
	s, err := host.New(host.NewArgs{Name: "frontend", Cwd: "~", Argv: []string{"sleep", "60"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	old := *bundle.AfterHostFacts
	t.Cleanup(func() { *bundle.AfterHostFacts = old })
	o := bundle.Options{Host: &ch, Dir: t.TempDir(), Session: "frontend"}
	hintOf := func(ctx context.Context, what, code string) string {
		t.Helper()
		_, err := bundle.Write(ctx, o)
		var ue *client.UserError
		if !errors.As(err, &ue) || ue.Code != code {
			t.Fatalf("%s: %v", what, err)
		}
		return ue.Hint
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := hintOf(ctx, "stopped at once", "cancelled"); !strings.HasPrefix(got, brand.Name+" ls box ") {
		t.Errorf("a bundle that stopped before it found the session: %q", got)
	}

	ctx, cancel = context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	*bundle.AfterHostFacts = cancel
	if got := hintOf(ctx, "stopped after the lookup", "cancelled"); got != brand.Name+" debug bundle box/"+s.ID+" (to start it again)" {
		t.Errorf("a bundle that stopped after it found the session: %q", got)
	}

	ctx, cancel = context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	*bundle.AfterHostFacts = func() { tr.Drop() }
	if got := hintOf(ctx, "closed after the lookup", "closed"); got != brand.Name+" debug bundle box/"+s.ID+" (to start it again)" {
		t.Errorf("a bundle whose connection closed after it found the session: %q", got)
	}
}
