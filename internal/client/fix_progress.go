package client

import "context"

type localRemovalKey struct{}

// withLocalRemovalReport belongs to one synchronous local repair. It is
// observation only and must not grant removal or outlive that repair.
func withLocalRemovalReport(ctx context.Context, report func(string)) context.Context {
	return context.WithValue(ctx, localRemovalKey{}, report)
}

// reportLocalRemoval runs only after successful owned unlink. Missing,
// replaced, live, referenced or foreign resources never report a change.
func reportLocalRemoval(ctx context.Context, path string) {
	if report, ok := ctx.Value(localRemovalKey{}).(func(string)); ok {
		report(path)
	}
}
