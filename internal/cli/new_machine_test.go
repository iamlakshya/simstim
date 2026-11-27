package cli

import (
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestMachineNewRefusesOtherProvidersAndPlacementBeforeCreate(t *testing.T) {
	for _, test := range []struct {
		provider, flag string
		args           []string
		code           string
	}{
		{provider: "lima", code: "machine_provider"},
		{provider: "docker", code: "machine_provider"},
		{provider: "e2b", args: []string{"box"}, code: "flags_conflict"},
		{provider: "e2b", flag: "dry-run", code: "flags_conflict"},
		{provider: "e2b", flag: "allow-wake", code: "flags_conflict"},
		{provider: "e2b", flag: "in", code: "flags_conflict"},
	} {
		t.Run(test.provider+"/"+test.flag, func(t *testing.T) {
			cmd := newCmd()
			if test.flag != "" {
				value := "true"
				if test.flag == "in" {
					value = "root"
				}
				if err := cmd.Flags().Set(test.flag, value); err != nil {
					t.Fatal(err)
				}
			}
			err := checkNewMachineFlagsV1(cmd, test.args, test.provider)
			var userErr *client.UserError
			if !errors.As(err, &userErr) || userErr.Code != test.code || userErr.Hint == "" {
				t.Fatalf("got %v, want %s and a hint", err, test.code)
			}
		})
	}
}

func TestMachineNewAcceptsIndependentCountAndExplicitDowngrade(t *testing.T) {
	cmd := newCmd()
	for flag, value := range map[string]string{"count": "3", "wall": "process", "downgrade": "true"} {
		if err := cmd.Flags().Set(flag, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := checkNewMachineFlagsV1(cmd, nil, "e2b"); err != nil {
		t.Fatal(err)
	}
}
