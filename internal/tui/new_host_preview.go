package tui

import (
	"context"
	"errors"
	"slices"

	"github.com/iamlakshya/simstim/internal/client"
)

// hostSelectionPreview is display data. A Host snapshot is metadata only.
// No field holds a request, connection, reservation or permission to start.
type hostSelectionPreview struct {
	host            client.Host
	names           []string
	automatic       bool
	reason          string
	defaulted       bool
	facadeValidated bool // skips only the launcher's duplicate default refusal
}

func (p hostSelectionPreview) neutral() bool { return p.automatic || len(p.names) > 1 }

func sameHostPreview(a, b hostSelectionPreview) bool {
	if a.automatic != b.automatic || a.defaulted != b.defaulted || a.reason != b.reason || !slices.Equal(a.names, b.names) {
		return false
	}
	return a.neutral() || client.SameEntry(a.host, b.host)
}

// PreviewHostSelection closes its actual metadata facade before publishing
// anything. The start owner must prepare its own original selection and make
// fresh attempts; this preview cannot bind a request or select an Auto host.
func (realBackend) PreviewHostSelection(ctx context.Context, selected string, options client.NewInputOptions) (hostSelectionPreview, error) {
	selection, err := client.PrepareHostSelection(ctx, selected, options)
	if err != nil {
		return hostSelectionPreview{}, err
	}
	var preview hostSelectionPreview
	err = selection.Check(ctx)
	if err == nil {
		preview.names, preview.automatic, preview.reason = selection.Names(), selection.Automatic(), selection.Reason()
		// Typed display metadata; reason strings never decide whether a rule
		// is a default or whether any action has been approved.
		preview.defaulted = selection.Defaulted()
		if !preview.neutral() && len(preview.names) == 1 {
			preview.host, err = client.ResolveHost(ctx, preview.names[0])
		}
	}
	if err == nil {
		err = selection.Check(ctx)
	}
	err = errors.Join(err, selection.Close())
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return hostSelectionPreview{}, err
	}
	preview.facadeValidated = true
	return preview, nil
}

// Legacy previews preserve the old fake/demo contract and its refusal path.
// In particular they never open the laptop's real settings or contact a host.
func legacyHostPreview(host client.Host, selected string) hostSelectionPreview {
	return hostSelectionPreview{host: host, names: []string{host.Name}, defaulted: selected == ""}
}

func (b *demoBackend) PreviewHostSelection(ctx context.Context, selected string, _ client.NewInputOptions) (hostSelectionPreview, error) {
	host, err := b.ResolveHost(ctx, selected)
	if err != nil {
		return hostSelectionPreview{}, err
	}
	return legacyHostPreview(host, selected), nil
}
