package tui

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func realRulePreview(t *testing.T, selector *api.HostSelectorV1) func(context.Context, string, client.NewInputOptions) (hostSelectionPreview, error) {
	t.Helper()
	cwd := t.TempDir()
	return func(ctx context.Context, selected string, options client.NewInputOptions) (hostSelectionPreview, error) {
		options.OriginalCWD = cwd
		options.Session = api.LayerValues{Sandbox: api.SandboxValues{Host: selector}}
		return (realBackend{}).PreviewHostSelection(ctx, selected, options)
	}
}

func TestRealHostPreviewUsesTheActualFacadeAndPublishesOnlyMetadata(t *testing.T) {
	hs := newPlacementFixture(t, "atlas-dev")
	options := client.NewInputOptions{OriginalCWD: t.TempDir(), Session: api.LayerValues{Sandbox: api.SandboxValues{
		Host: &api.HostSelectorV1{Names: []string{"gcp-dev", "atlas-dev"}},
	}}}
	preview, err := (realBackend{}).PreviewHostSelection(t.Context(), "", options)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.facadeValidated || preview.defaulted || preview.automatic || !preview.neutral() ||
		preview.host.Name != "" || !slices.Equal(preview.names, []string{"gcp-dev", "atlas-dev"}) || preview.reason != "from the session's host rule" {
		t.Fatal("the real facade's ordered display metadata was lost", preview)
	}
	if len(hs.be.newArgs) != 0 || len(hs.be.secretsFrom) != 0 || len(hs.be.attached) != 0 {
		t.Fatal("a preview reached starts, values or terminal ownership")
	}
	for _, calls := range hs.be.dials {
		if calls != 1 {
			t.Fatal("a metadata preview opened another host connection", hs.be.dials)
		}
	}
}

func TestAutoAndOrderedRuleFormsHaveNoHighlightedHost(t *testing.T) {
	for _, selector := range []*api.HostSelectorV1{
		{Auto: true}, {Names: []string{"gcp-dev", "atlas-dev"}},
	} {
		hs := newPlacementFixture(t, "atlas-dev")
		hs.be.previewHost = realRulePreview(t, selector)
		hs.m.hostSel = 2 // an offline highlighted host supplies no target or agents
		hs.key("n")
		d, ok := hs.m.modal.(*newSession)
		if !ok || d.host != "" || d.sourceHost.Name != "" || d.defaultHost != "" || !d.preview.neutral() || d.finding || hs.m.prompt != nil {
			t.Fatalf("a metadata rule acquired a concrete UI target: %T", hs.m.modal)
		}
		if !strings.Contains(plainScreen(hs.m), "from the session's host rule") || strings.Contains(d.hostRow(hs.m, 80), "default") {
			t.Fatal("the form hid the rule or labelled it as the default")
		}
		d.name.set("kept-name")
		d.folder.set("~/kept-folder")
		d.command.set("kept command")
		d.worktree, d.wall, d.field = true, "microvm", 6
		hs.key("right")
		if d.selector != "atlas-dev" || d.host != "atlas-dev" || d.sourceHost.Name != "atlas-dev" ||
			d.name.value() != "kept-name" || d.folder.value() != "~/kept-folder" || d.command.value() != "kept command" || !d.worktree || d.wall != "microvm" {
			t.Fatal("an explicit host did not replace metadata intent while keeping edits")
		}
		if len(hs.be.newArgs) != 0 || len(hs.be.secretsFrom) != 0 {
			t.Fatal("editing a metadata selection started work")
		}
	}
}

func TestSharedRuleSkipsOnlyTheDuplicateDefaultRefusal(t *testing.T) {
	hs := newPlacementFixture(t, "atlas-dev")
	if err := client.SetTierContext(t.Context(), "gcp-dev", client.TierShared, strings.Repeat("b", 32), ""); err != nil {
		t.Fatal(err)
	}
	t.Setenv(brand.Env("DEFAULT_HOST"), "gcp-dev")
	hs.be.previewHost = realRulePreview(t, &api.HostSelectorV1{Names: []string{"gcp-dev"}})
	hs.key("n")
	d, ok := hs.m.modal.(*newSession)
	if !ok || d.host != "gcp-dev" || d.preview.defaulted || d.defaultHost != "" {
		t.Fatalf("a valid explicit rule was treated as a shared default: %T", hs.m.modal)
	}
	if strings.Contains(d.hostRow(hs.m, 80), "default") || len(hs.be.newArgs) != 0 {
		t.Fatal("a rule gained a false default label or reached a start")
	}
	// The same real facade must continue to refuse the canonical shared default.
	_, err := (realBackend{}).PreviewHostSelection(t.Context(), "", client.NewInputOptions{OriginalCWD: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "shared host") {
		t.Fatal("bypassing the duplicate UI refusal bypassed the canonical refusal", err)
	}
}

func TestCancelledAutoPreviewCannotOpenALateNeutralForm(t *testing.T) {
	hs := newPlacementFixture(t, "atlas-dev")
	actual := realRulePreview(t, &api.HostSelectorV1{Auto: true})
	preview, err := actual(t.Context(), "", client.NewInputOptions{})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan context.Context, 1), make(chan struct{})
	var once sync.Once
	releasePreview := func() { once.Do(func() { close(release) }) }
	defer releasePreview()
	hs.be.previewHost = func(ctx context.Context, _ string, _ client.NewInputOptions) (hostSelectionPreview, error) {
		entered <- ctx
		<-release // an old worker may return metadata after cancellation
		return preview, nil
	}
	hs.start(hs.m.openQuickStart())
	ctx := waitNewHostQuery(t, entered)
	hs.deliver(keyMsg("esc"))
	if ctx.Err() == nil {
		t.Fatal("Escape left the preview owner live")
	}
	releasePreview()
	hs.do(nil)
	if hs.m.modal != nil || hs.m.prompt != nil || len(hs.be.newArgs) != 0 || len(hs.be.attached) != 0 {
		t.Fatal("retired Auto metadata opened a form or started work")
	}
}

func TestRealDefaultPreviewKeepsH12SelectionAcrossEveryView(t *testing.T) {
	for _, view := range []struct {
		screen  screen
		current string
	}{{scrHosts, ""}, {scrSessions, "atlas-dev"}, {scrSessions, ""}} {
		hs := newPlacementFixture(t, "gcp-dev")
		cwd := t.TempDir()
		hs.be.previewHost = func(ctx context.Context, selected string, options client.NewInputOptions) (hostSelectionPreview, error) {
			options.OriginalCWD = cwd
			return (realBackend{}).PreviewHostSelection(ctx, selected, options)
		}
		hs.m.screen, hs.m.cur, hs.m.hostSel = view.screen, view.current, 2
		hs.m.mem.Last = &lastSession{Host: "aws-gpu", ID: "last2345"}
		hs.key("n")
		d, ok := hs.m.modal.(*newSession)
		if !ok || d.host != "gcp-dev" || d.sourceHost.Name != "gcp-dev" || d.defaultHost != "gcp-dev" || d.selector != "" {
			t.Fatalf("view %v replaced the canonical default: %T", view, hs.m.modal)
		}
		if !strings.Contains(d.hostRow(hs.m, 80), "gcp-dev (default)") || len(hs.be.newArgs) != 0 {
			t.Fatal("the default identity disappeared or a preview started work")
		}
	}
}
