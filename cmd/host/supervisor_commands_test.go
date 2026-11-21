package main

import "testing"

func TestSupervisorCommandsHaveProductionEntrypoints(t *testing.T) {
	for _, name := range []string{"supervise", "supervise-restore-v1"} {
		run := commands[name]
		if run == nil {
			t.Fatalf("missing production %s entrypoint", name)
		}
		for _, args := range [][]string{nil, {"invalid"}, {"abcdefgh", "extra"}} {
			if err := run(args); err == nil {
				t.Fatalf("%s accepted invalid closed arguments %q", name, args)
			}
		}
	}
}
