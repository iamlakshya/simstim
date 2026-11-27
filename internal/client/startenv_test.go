package client

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestStartEnvironmentPreservesFlagOrderAndLiteralValues(t *testing.T) {
	layer := map[string]string{"LEVEL": "layer", "UNCHANGED": "yes"}
	inputs := envInputs{lookup: func(name string) (string, bool) { return "from laptop", name == "FROM_LAPTOP" }, readFile: func(context.Context, string) ([]byte, error) {
		return []byte("# comment\nLEVEL=file\nQUOTED=\"$HOME,=literal\"\nFROM_LAPTOP\n"), nil
	}}
	got, err := resolveStartEnvironment(t.Context(), layer, []envOption{{value: "LEVEL=first"}, {value: "fixture", file: true}, {value: "LEVEL=last"}}, inputs)
	want := map[string]string{"LEVEL": "last", "UNCHANGED": "yes", "QUOTED": "\"$HOME,=literal\"", "FROM_LAPTOP": "from laptop"}
	if err != nil || !reflect.DeepEqual(got, want) || layer["LEVEL"] != "layer" {
		t.Fatal(got, err)
	}
}

func TestStartEnvironmentRejectsAllConfiguredNamesBeforeLookup(t *testing.T) {
	looked := false
	inputs := envInputs{configured: map[string]bool{"UNREQUESTED_KEY": true}, lookup: func(string) (string, bool) { looked = true; return "must not read", true }}
	_, err := resolveStartEnvironment(t.Context(), nil, []envOption{{value: "UNREQUESTED_KEY"}}, inputs)
	var refused *UserError
	if !errors.As(err, &refused) || refused.Code != "env_is_secret" || looked {
		t.Fatal("configured name escaped the all-name barrier", err, looked)
	}
	if _, err = resolveStartEnvironment(t.Context(), map[string]string{"UNREQUESTED_KEY": "literal"}, nil, inputs); err == nil {
		t.Fatal("layer value bypassed the configured-name barrier")
	}
	if _, err = resolveStartEnvironment(t.Context(), nil, []envOption{{value: "SSH_AUTH_SOCK=/tmp/fixture"}}, inputs); err == nil {
		t.Fatal("reserved socket entered ordinary env")
	}
}

func TestStartEnvironmentMissingAndCancelledInputsPublishNothing(t *testing.T) {
	if _, err := resolveStartEnvironment(t.Context(), nil, []envOption{{value: "MISSING"}}, envInputs{lookup: func(string) (string, bool) { return "", false }}); err == nil {
		t.Fatal("missing laptop value silently became empty")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	read := false
	got, err := resolveStartEnvironment(ctx, nil, []envOption{{value: "file", file: true}}, envInputs{readFile: func(context.Context, string) ([]byte, error) { read = true; return nil, nil }})
	if got != nil || !errors.Is(err, context.Canceled) || read {
		t.Fatal(got, err, read)
	}
}
