package client

import (
	"context"

	"github.com/iamlakshya/simstim/internal/runtimecache"
)

type runtimeFetchKey struct{}

// WithRuntimeFetch applies an explicit fetch override to one host operation.
// The preference remains in memory and keeps the existing startup context.
func WithRuntimeFetch(ctx context.Context, mode string) context.Context {
	return context.WithValue(ctx, runtimeFetchKey{}, mode)
}

// RuntimeFetchPreference reads a command override, then the images setting.
// It validates settings before any runtime probe or network operation.
func RuntimeFetchPreference(ctx context.Context) (string, error) {
	settings := LoadSettings()
	if err := ValidateSettings(settings); err != nil {
		return "", err
	}
	mode := settings.Images.Fetch
	if override, ok := ctx.Value(runtimeFetchKey{}).(string); ok && override != "" {
		mode = override
	}
	return RuntimeFetchMode(mode)
}

// HasReleaseRuntime reports whether this client's manifest names files for
// runtime delivery. Helper-only development builds need no runtime dial.
func HasReleaseRuntime() (bool, error) {
	m, err := ReleaseManifest()
	if err != nil {
		return false, err
	}
	for _, item := range m.Items {
		if runtimecache.IsRuntime(item.Kind) {
			return true, nil
		}
	}
	return false, nil
}
