//go:build linux

package main

import (
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestMicroVMCommandsRefusePathArguments(t *testing.T) {
	for _, name := range []string{"chv-disk", "chv-run", "chv-exec"} {
		t.Run(name, func(t *testing.T) {
			run := commands[name]
			if run == nil {
				t.Fatal("the production helper cannot enter its VM stage", name)
			}
			// The refusal must happen before no-dump, descriptor handling,
			// filesystem preparation or a process start in the real stage.
			err := run([]string{"/unexpected-plan"})
			var failure *proto.Error
			if !errors.As(err, &failure) || failure.Code != "wall_init" || failure.Hint == "" {
				t.Fatal("a command argument reached a closed VM stage", err)
			}
		})
	}
}
