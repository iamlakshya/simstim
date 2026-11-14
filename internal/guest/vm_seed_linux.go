//go:build linux

package guest

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (v *vmRuntime) seedFile(file *os.File) error {
	return v.files.ops.own(file, AgentUID, AgentGID)
}
func (v *vmRuntime) seedDirectory(path string) error {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close()
	return vmOwnedDirectory(root, filepath.Base(path), 0o700, AgentUID, AgentGID, v.files.ops.own)
}

// seedGit runs image Git only through the SAME fixed1000 owned runner. A
// root init never executes the image Git binary with inherited root rights.
func (v *vmRuntime) seedGit(dir string, args []string) error {
	ctx, cancel := context.WithTimeout(v.ctx, time.Minute)
	defer cancel()
	argv := append([]string{"/usr/bin/git"}, host.SafeGitArgs(dir, args...)...)
	env := []string{"PATH=/usr/bin:/bin", "HOME=/home/agent", "XDG_CONFIG_HOME=/home/agent/.config", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "LANG=C.UTF-8"}
	run := newOwnedCommand(ctx, proto.CtlExecArgs{Argv: argv, Cwd: dir}, env)
	run.vmRole = vmSeedCommand
	defer run.cancel()
	sink := vmSeedOutput{}
	code := v.init.executeOwned(run, ownedCommandIO{Out: sink, Err: sink})
	if code != 0 {
		return vmError("the image's unprivileged Git could not seed the workspace")
	}
	return nil
}

// vmSeedOutput drains diagnostics without retaining bundle- or image-chosen
// text. IO.Copy in the one runner remains bounded by its ordinary buffers.
type vmSeedOutput struct{}

func (vmSeedOutput) Write(data []byte) (int, error) { return len(data), nil }

func (v *vmRuntime) seedStream(stream *link.Stream) (func(), error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.ctx.Err() != nil {
		return nil, vmError("the VM stopped before its seed IO began")
	}
	if v.seedIO == nil {
		v.seedIO = map[*link.Stream]struct{}{}
	}
	v.seedIO[stream] = struct{}{}
	v.seedWG.Add(1)
	return func() { v.mu.Lock(); delete(v.seedIO, stream); v.mu.Unlock(); v.seedWG.Done() }, nil
}

func (v *vmRuntime) childEnvironment(input []string) []string {
	env := append([]string(nil), input...)
	if v.plan.Args.VM.Docker {
		env = append(env, "DOCKER_HOST=unix:///run/"+brand.Name+"/docker.sock")
	}
	return env
}
