package client

import (
	"context"
	"os"
	"runtime"
)

// EnvironmentOption is one --env or --env-file encounter from the existing
// command parser. Value remains literal. File selects a local env file.
type EnvironmentOption struct {
	Value string
	File  bool
}

// ResolveSessionEnvironment checks the canonical complete local settings view
// before any env-file read or laptop lookup. Resolved contains the environment
// produced by the SAME policy merger. This function runs no value command,
// grants no host permission and publishes nothing.
func ResolveSessionEnvironment(ctx context.Context, settings Settings, resolved map[string]string, options []EnvironmentOption) (map[string]string, error) {
	names, err := ConfiguredSecretNames(settings)
	if err != nil {
		return nil, err
	}
	ordered := make([]envOption, 0, len(options))
	for _, option := range options {
		ordered = append(ordered, envOption{value: option.Value, file: option.File})
	}
	return resolveStartEnvironment(ctx, resolved, ordered, envInputs{
		configured: names, lookup: os.LookupEnv, lookupFold: runtime.GOOS == "windows", readFile: readEnvironmentFile,
	})
}
