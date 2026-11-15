//go:build linux

package main

import (
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestProductionWallEntryRejectsOtherInvocationsBeforeProbe(t *testing.T) {
	run := commands["wall"]
	if run == nil {
		t.Fatal("the production command registry omitted the wall child entry")
	}
	for _, args := range [][]string{nil, {"probe"}, {"probe-child", "extra"}, {"probe-child", "--json"}, {"other"}} {
		err := run(args)
		var refusal *proto.Error
		if !errors.As(err, &refusal) || refusal.Code != "wall_probe" || refusal.Hint == "" {
			t.Fatalf("wall %q did not refuse before the native probe: %v", args, err)
		}
	}
}
