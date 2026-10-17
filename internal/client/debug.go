package client

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/version"
)

// The client's debug mode (decision 0025). The flags --debug and
// --debug-file, or the variable diag.EnvFile, turn it on for the whole
// process: its records, its debug lines and, with the runtime category, its
// own profiles and flight recorder. Each connection it opens asks its
// bridge to record too. With debug off, each hook costs one nil check, and
// each debug line a call to a handler that discards it.

// debugRun is the process's debug run, or nil while debug is off.
var debugRun atomic.Pointer[diag.Run]

// debugFileMax is the size at which the client's debug file turns over.
// Tests lower it.
var debugFileMax int64 = diag.DefaultMax

// DebugDir is the laptop's debug folder: $XDG_STATE_HOME/<name>/debug, or
// ~/.local/state/<name>/debug.
func DebugDir() string { return filepath.Join(StateDir(), "debug") }

// DebugOptions turns on the client's debug.
type DebugOptions struct {
	// Cats are the categories to record.
	Cats diag.Cats
	// File is the debug file, or "" for a new one in DebugDir.
	File string
}

// StartDebug turns on the client's debug for the rest of the process, and
// returns the path of its debug file. StopDebug ends it.
func StartDebug(o DebugOptions) (string, error) {
	now := time.Now()
	dir := DebugDir()
	if err := diag.EnsureDir(dir); err != nil {
		return "", &UserError{Msg: "cannot make the debug folder: " + err.Error(), Hint: "Check that you can write " + dir + "."}
	}
	prefix := fmt.Sprintf("client-%s-%d", diag.TimeName(now), os.Getpid())
	path := o.File
	if path == "" {
		path = diag.FreePath(dir, prefix, ".jsonl")
	} else if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	run, err := diag.StartRun(o.Cats, diag.RunOptions{
		Path:    path,
		Max:     debugFileMax,
		Start:   diag.Record{Side: "client", Version: version.Version, Wall: now.UnixNano()},
		Runtime: diag.RuntimeOptions{Dir: dir, Max: diag.FolderMax, Prefix: prefix + "-", CPU: true, Flight: true},
	})
	if err != nil {
		return "", &UserError{Msg: "cannot write the debug file: " + err.Error(),
			Hint: "Pass --debug-file with a file you can write, or unset " + diag.EnvFile + "."}
	}
	if filepath.Dir(path) != dir {
		noteOutside(dir, path)
	}
	debugRun.Store(run)
	run.Log.Debug("debug on", "cats", o.Cats.String(), "version", version.Version)
	return path, nil
}

// noteOutside adds path to the list of debug files outside the debug
// folder, for the bundle, once.
func noteOutside(dir, path string) {
	list := filepath.Join(dir, diag.OutsideList)
	old, _ := os.ReadFile(list)
	if slices.Contains(strings.Split(string(old), "\n"), path) {
		return
	}
	f, err := os.OpenFile(list, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintln(f, path)
}

// StopDebug ends the client's debug: it writes the run's profiles and
// closes its file. When a write failed, as on a full disk, it says so on
// w, so that a file cut short does not pass unseen.
func StopDebug(w io.Writer) {
	run := debugRun.Swap(nil)
	if run == nil {
		return
	}
	if err := run.Stop("exit"); err != nil {
		fmt.Fprintf(w, "Warning: the debug file %s is not complete: %v\nTry: make room on its disk, or pass --debug-file with a file on another disk.\n",
			run.File.Path(), err)
	}
}

// DebugLog is the logger of the client's debug lines. With debug off, it
// discards them.
func DebugLog() *slog.Logger {
	if r := debugRun.Load(); r != nil {
		return r.Log
	}
	return diag.Discard
}

// DebugOn reports whether the client records the category name.
func DebugOn(name string) bool { return debugRun.Load().On(name) }

// DebugFile is the client's debug file while debug is on, or nil.
func DebugFile() *diag.File {
	if r := debugRun.Load(); r != nil {
		return r.File
	}
	return nil
}

// debugRuntime is the runtime category's work in the client, or nil.
func debugRuntime() *diag.Runtime { return debugRun.Load().Runtime() }

// startBridgeDebug asks the bridge of a new connection to record its side,
// when the client's debug has a category that a bridge records. It does
// not wait for the answer, which may come after the dial's ctx ends.
func (c *Conn) startBridgeDebug(ctx context.Context) {
	run := debugRun.Load()
	if run == nil {
		return
	}
	run.Log.Debug("connected", diag.CatKey, diag.CatNet, "host", c.Host.Name, "features", len(c.Features))
	cats := run.Cats() & diag.BridgeCats
	if cats == 0 || !c.HasFeature(api.FeatureDebug) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		var st api.DebugState
		if err := c.Call(ctx, api.OpDebugStart, api.DebugStartArgs{Categories: cats.List()}, &st); err != nil {
			run.Log.Debug("the bridge's debug did not start", diag.CatKey, diag.CatNet, "err", err)
			return
		}
		run.Log.Debug("the bridge's debug started", diag.CatKey, diag.CatNet, "file", st.File)
	}()
}

// stderrLines is where the stderr of a transport's command goes, with the
// net category on: each line becomes a debug line. It holds ssh's own
// errors. Without the category, it is nil.
func stderrLines() io.Writer {
	if !DebugOn(diag.CatNet) {
		return nil
	}
	return &lineLogger{}
}

// lineLogger writes each line written to it as a debug line.
type lineLogger struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lineLogger) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Write(p)
	for {
		line, err := l.buf.ReadString('\n')
		if err != nil {
			// No whole line yet: keep the rest for the next write.
			l.buf.Reset()
			l.buf.WriteString(line)
			return len(p), nil
		}
		if line = strings.TrimRight(line, "\r\n"); line != "" {
			DebugLog().Debug("stderr", diag.CatKey, diag.CatNet, "line", line)
		}
	}
}

// masterState records whether the master of the host's shared SSH
// connection runs, from `ssh -O check`, with the net category on. It
// records only yes or no, and the master's process ID.
func (s *SSH) masterState(ctx context.Context, when string) {
	f := DebugFile()
	if f == nil || !DebugOn(diag.CatNet) || !controlMaster || s.Host.SSH == "" {
		return
	}
	running, pid := s.CheckMaster(ctx)
	f.Add(diag.Record{T: diag.Now(), Cat: diag.CatNet, Ev: "master", Reason: when, Running: &running, PID: pid})
}

// parseMaster reads the answer of `ssh -O check`: "Master running (pid=1234)".
func parseMaster(out string) (running bool, pid int) {
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "Master running (pid="); ok {
			_, _ = fmt.Sscanf(rest, "%d)", &pid)
			return true, pid
		}
	}
	return false, 0
}
