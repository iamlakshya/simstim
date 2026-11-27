package client

import (
	"errors"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestUnspecifiedNewWallUsesThePinnedFloor(t *testing.T) {
	for _, level := range []string{"process", "gvisor", "microvm"} {
		got, err := ChooseWall(Host{Name: "box", WallFloor: level}, TierOwn, "", false)
		if err != nil || got.Level != level || got.Floor != level || got.DowngradedFrom != "" {
			t.Fatal("unspecified start did not use its actual pinned floor", level, got, err)
		}
	}
}

func TestExplicitNoneNeedsAndRecordsDowngradeEvenAtNoneFloor(t *testing.T) {
	h := Host{Name: "box", WallFloor: "none"}
	if _, err := ChooseWall(h, TierOwn, "none", false); err == nil {
		t.Fatal("explicit none skipped downgrade at a none floor")
	}
	got, err := ChooseWall(h, TierOwn, "none", true)
	if err != nil || got.Level != "none" || got.Floor != "none" || got.DowngradedFrom == "" {
		t.Fatal("intentional none lost its real recorded downgrade", got, err)
	}
}

func TestSharedAndUnknownProcessCannotUseADowngradeSheet(t *testing.T) {
	for _, tier := range []Tier{TierShared, TierUnknown} {
		_, err := ChooseWall(Host{Name: "box", WallFloor: "gvisor"}, tier, "process", true)
		var floor *WallFloorError
		if err == nil || errors.As(err, &floor) {
			t.Fatal("a tier prohibition became a waivable floor sheet", tier, err)
		}
	}
}

func TestDefaultWallScalarIsOwnedAndPreservesExplicitNone(t *testing.T) {
	a, b := DefaultSettings(), DefaultSettings()
	if a.Sandbox.Wall == nil || b.Sandbox.Wall == nil || *a.Sandbox.Wall != "auto" || *b.Sandbox.Wall != "auto" {
		t.Fatal("settings default is not pinned-floor auto")
	}
	*a.Sandbox.Wall = "none"
	if *b.Sandbox.Wall != "auto" {
		t.Fatal("one caller changed another default selection")
	}
	none := "none"
	configured := withDefaults(Settings{LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Wall: &none}}})
	if configured.Sandbox.Wall == nil || *configured.Sandbox.Wall != "none" {
		t.Fatal("defaults overwrote an explicit local layer")
	}
}

func defaultWallSourceFixture(t *testing.T, floor string) (*Conn, *GrantRequest, *int) {
	t.Helper()
	h := h5GrantState(t)
	h.WallFloor = floor
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	c := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), "session.id", "sandbox", "wall.process", "wall.gvisor", "wall.microvm")
	c.pending = map[uint64]chan proto.Response{}
	calls := new(int)
	sharedGrantWire(t, c, func(req proto.Request) any {
		*calls++
		if req.Op != "sandbox.probe" {
			t.Fatal("wall selection issued an unrelated operation", req.Op)
		}
		return api.CapProbe{Walls: []api.WallProbe{{Level: "gvisor", Driver: "gvisor", OK: true}, {Level: "microvm", Driver: "chv", OK: true}}}
	})
	r := NewGrantRequest(false, false)
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	return c, r, calls
}

func TestActualWallResolverUsesProbeOnlyToCheckPinnedSelection(t *testing.T) {
	c, r, calls := defaultWallSourceFixture(t, "gvisor")
	args, err := c.ResolveNewWall(WithGrantRequest(t.Context(), r), NewArgs{})
	if err != nil || args.Wall != "gvisor" || args.Floor != "gvisor" || *calls != 1 {
		t.Fatal("a stronger fresh probe replaced the saved floor", args.Wall, args.Floor, *calls, err)
	}
}

func TestActualWallResolverRefusesMissingFloorBeforeProbeOrEffects(t *testing.T) {
	c, r, calls := defaultWallSourceFixture(t, "")
	_, err := c.ResolveNewWall(WithGrantRequest(t.Context(), r), NewArgs{})
	var user *UserError
	if !errors.As(err, &user) || user.Code != "wall_floor" || !strings.Contains(user.Hint, "host prepare") || *calls != 0 {
		t.Fatal("missing pinned floor silently adopted a host answer", *calls, err)
	}
}
