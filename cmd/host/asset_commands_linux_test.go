//go:build linux

package main

import (
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestProductionAssetEntriesRejectUnownedInvocationsBeforeEffects(t *testing.T) {
	cases := map[string][][]string{
		"image-guardian":  {nil, {"1"}, {"1", "other.scope"}, {"-1", "other.scope"}, {"01", "other.scope"}},
		"image-convert":   {nil, {"1", "extra"}, {"-1"}, {"01"}, {"4294967296"}},
		"agents-guardian": {nil, {"other.scope"}, {"other.scope", "extra"}},
		"agents-convert":  {{"extra"}, {"3"}},
	}
	for name, values := range cases {
		run := commands[name]
		if run == nil {
			t.Fatalf("the production command registry omitted %s", name)
		}
		for _, args := range values {
			var refusal *proto.Error
			if err := run(args); !errors.As(err, &refusal) || refusal.Code != "asset_entry" || refusal.Hint == "" {
				t.Fatalf("%s %q reached an asset producer without its closed invocation: %v", name, args, err)
			}
		}
	}
}
