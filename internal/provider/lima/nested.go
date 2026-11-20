package lima

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/provider"
)

// nestedRefusals are the two messages of Lima's vz driver when Apple says
// that this Mac cannot run a VM inside a VM (pkg/driver/vz/vm_darwin.go:429
// and 433 in Lima 2.3.0).
var nestedRefusals = []string{
	"nested virtualization is not supported on this device",
	"nested virtualization requires macOS 15 or newer",
}

// How long nestedRefused waits for the host agent's last line: haWait in
// all, haGrace after ha.pid has gone, and haPoll between two reads.
var (
	haWait  = 10 * time.Second
	haGrace = time.Second
	haPoll  = 100 * time.Millisecond
)

// startNew starts a VM that Create made. When Lima refuses nested
// virtualisation, it turns nesting off and starts the VM once more. If that
// fails, it deletes the VM, which no host names yet.
func startNew(ctx context.Context, out io.Writer, host, inst string) error {
	fmt.Fprintf(out, "  Starting it: limactl start %s\n", inst)
	err := startVM(ctx, inst)
	if err == nil || !nestedRefused(ctx, inst, err) {
		return err
	}
	fmt.Fprintf(out, "  This Mac cannot run a VM inside a VM, so %s gets no microvm sandboxes.\n", host)
	again := brand.Name + " host create lima " + host + "   (to try again)"
	edit := []string{"edit", "--tty=false", "--set", ".nestedVirtualization = false", inst}
	fmt.Fprintf(out, "  Turning nested virtualisation off: limactl %s\n", shellLine(edit))
	if _, err := provider.Exec(ctx, install, "limactl", edit...); err != nil {
		if ctx.Err() != nil {
			return err
		}
		return discard(ctx, inst, fmt.Sprintf("Lima could not turn nested virtualisation off for %s (%v)", inst, err), again)
	}
	if err := checkNestingOff(ctx, inst, again); err != nil {
		return err
	}
	fmt.Fprintf(out, "  Starting it again: limactl start %s\n", inst)
	if err := startVM(ctx, inst); err != nil {
		if ctx.Err() != nil {
			return err
		}
		return discard(ctx, inst, fmt.Sprintf("%s did not start without nested virtualisation either (%v)", inst, err), again)
	}
	return nil
}

// checkNestingOff reads the instance after the edit. Lima's override.yaml
// wins over the edit (pkg/limayaml/defaults.go in Lima 2.3.0), and a start
// with nesting still on would fail again. So the VM starts again only after
// a read that shows nesting off. Any other read deletes the VM.
func checkNestingOff(ctx context.Context, inst, again string) error {
	in, err := find(ctx, inst)
	switch {
	case err != nil && ctx.Err() != nil:
		return err
	case err != nil:
		return discard(ctx, inst, fmt.Sprintf("could not read %s from limactl after the edit (%v)", inst, err), again)
	case in == nil:
		// A list that leaves the VM out may still hide one that no host names.
		return discard(ctx, inst, "limactl did not list "+inst+" after the edit", again)
	case in.Config == nil || in.Config.NestedVirtualization == nil:
		return discard(ctx, inst, "limactl did not say whether nested virtualisation is off for "+inst, again)
	case *in.Config.NestedVirtualization:
		return discard(ctx, inst, fmt.Sprintf("Lima's override.yaml keeps nested virtualisation on for %s, which this Mac cannot run", inst),
			"remove nestedVirtualization from "+overridePath()+", then run this again")
	}
	return nil
}

func startVM(ctx context.Context, inst string) error {
	_, err := provider.Exec(ctx, install, "limactl", "start", "--tty=false", inst)
	return err
}

// nestedRefused reports whether a failed start was Lima's refusal of nested
// virtualisation. The host agent logs the refusal to ha.stderr.log in the
// instance's folder. `limactl start` passes that line on to its own stderr
// only when it reads the line before the host agent's "exiting" event,
// which comes first (pkg/hostagent/events/watcher.go:75-83). So the full
// stderr is read first, then the log.
func nestedRefused(ctx context.Context, inst string, err error) bool {
	text := err.Error()
	var ee *provider.ExecError
	if errors.As(err, &ee) {
		text += "\n" + ee.Stderr
	}
	if refusal(text) {
		return true
	}
	in, ferr := find(ctx, inst)
	if ferr != nil || in == nil || in.Dir == "" {
		return false
	}
	return refusal(hostAgentLog(ctx, in.Dir))
}

func refusal(text string) bool {
	for _, m := range nestedRefusals {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

// hostAgentLog reads ha.stderr.log of a VM whose start failed. Lima's host
// agent sends the "exiting" event that ends `limactl start`, then removes
// ha.pid, and only then logs its error (pkg/hostagent/hostagent.go:424-434,
// cmd/limactl/hostagent.go:59, cmd/limactl/main.go:56). So the log may not
// hold the error yet. It is read again until it holds a fatal line, or the
// host agent's process has gone, or for haGrace after ha.pid has gone, and
// for haWait at most.
func hostAgentLog(ctx context.Context, dir string) string {
	deadline := time.Now().Add(haWait)
	var gone time.Time
	for {
		// The process is checked before the log is read: a log read after
		// the process has gone is complete.
		pid, hasPID := haPID(dir)
		exited := hasPID && !alive(pid)
		b, _ := os.ReadFile(filepath.Join(dir, "ha.stderr.log"))
		log := string(b)
		now := time.Now()
		if !hasPID && gone.IsZero() {
			gone = now
		}
		if fatal(log) || exited || (!gone.IsZero() && now.Sub(gone) >= haGrace) || now.After(deadline) {
			return log
		}
		select {
		case <-ctx.Done():
			return log
		case <-time.After(haPoll):
		}
	}
}

// fatal reports whether a host agent's log holds a fatal line, in the JSON
// that the host agent writes (cmd/limactl/hostagent.go:201) or as text.
func fatal(log string) bool {
	return strings.Contains(log, `"level":"fatal"`) || strings.Contains(log, "level=fatal")
}

// haPID reads the host agent's PID from ha.pid in dir. It reports false when
// there is no such file.
func haPID(dir string) (int, bool) {
	b, err := os.ReadFile(filepath.Join(dir, "ha.pid"))
	if err != nil {
		return 0, false
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid, true
}

// alive reports whether the process pid runs. Where signal 0 cannot be
// sent, as on Windows, it reports false.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
