// Package none runs the ordinary guest init without creating a wall.
package none

import (
	"context"
	"path/filepath"
	"runtime"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/wall"
)

type Driver struct{}

func (Driver) Name() string      { return "none" }
func (Driver) Level() wall.Level { return wall.None }
func (Driver) Probe(ctx context.Context) wall.Probe {
	p := wall.Probe{Driver: "none", Level: wall.None, OK: true, CanPrepare: true}
	if runtime.GOOS != "linux" {
		p.OK = false
		p.CanPrepare = false
		p.Reason = "the supervisor host runtime requires Linux"
	}
	if err := ctx.Err(); err != nil {
		p.OK = false
		p.CanPrepare = false
		p.Reason = err.Error()
	}
	return p
}
func (Driver) Prepare(ctx context.Context, spec wall.Spec) (wall.Sandbox, error) {
	if runtime.GOOS != "linux" {
		return nil, &proto.Error{Code: "wall_setup", Message: "the unwalled supervisor needs a Linux host", Hint: "Use a Linux host or a Linux VM; the client may still run on this computer."}
	}
	if !api.ValidSessionID(spec.ID) || !filepath.IsAbs(spec.SessionDir) || filepath.Clean(spec.SessionDir) != spec.SessionDir || filepath.Base(spec.SessionDir) != spec.ID || spec.Image != nil || spec.VM != nil || spec.RuntimeManifest != nil || len(spec.Mounts) != 0 || len(spec.StateDirs) != 0 || len(spec.StateFiles) != 0 || spec.StateRestore != "" || spec.StateCapture != "" || spec.DiskMax != 0 {
		return nil, failure("the unwalled runtime has a conflicting sandbox or storage plan", nil)
	}
	return newSandbox(ctx, spec)
}
func (driver Driver) Open(ctx context.Context, sessionDir string) (wall.Sandbox, error) {
	return driver.Prepare(ctx, wall.Spec{ID: filepath.Base(sessionDir), SessionDir: sessionDir})
}

func failure(message string, cause error) error {
	if cause != nil {
		message += ": " + cause.Error()
	}
	return &proto.Error{Code: "wall_setup", Message: message, Hint: "Inspect the session's original supervisor and runtime folder, then resume it with the current helper."}
}
