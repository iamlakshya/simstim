package tui

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestStartInputChoicesOwnTheirOriginalWallAndLists(t *testing.T) {
	a := client.NewArgs{Wall: "process", Mounts: []string{"/source"}, Include: []string{"config.json"}, Cwd: "~"}
	options := startInputOptions(a, false)
	a.Mounts[0] = "/changed"
	a.Include[0] = "changed.json"
	a.Wall = "microvm"
	if options.HostClone || options.OriginalCWD != "" || *options.Session.Sandbox.Wall != "process" || options.Session.Sandbox.Mounts[0] != "/source" || options.Session.Sandbox.Include[0] != "config.json" {
		t.Fatalf("original input choices changed: %+v", options)
	}
}
