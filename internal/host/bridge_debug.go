package host

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// The bridge's part of the debug mode (decision 0025). The feature "debug"
// adds the requests debug.start, debug.stop, debug.profile and debug.save.
// A client whose debug is on sends debug.start with no session on each of
// its connections: the bridge then records each request it serves, each
// attach and detach, and the size of each upload, until debug.stop or the
// end of the connection. A session's requests go to its holder.

func init() { Features = append(Features, api.FeatureDebug) }

// holderWait is how long the bridge waits for a holder's answer to a debug
// request, beyond a CPU profile's own time.
const holderWait = 10 * time.Second

// bridgeDebug is a bridge's own debug run, while it is on.
type bridgeDebug struct {
	mu    sync.Mutex // orders starting and stopping the run
	run   atomic.Pointer[diag.Run]
	lines *diag.Lines // the bridge's debug lines, into the run's file
	// pending are the requests in flight while the run is on, by ID: their
	// op and when the bridge read them.
	pending sync.Map
}

// pendingRequest is a request in flight, for its record.
type pendingRequest struct {
	op    string
	start int64
}

// liveBridges are the bridges that serve now, for stopBridgesDebug.
var liveBridges sync.Map

// stopBridgesDebug ends the debug of every bridge in this process, before
// an exit that skips their defers, as when a bridge is idle too long.
func stopBridgesDebug(reason string) {
	liveBridges.Range(func(k, _ any) bool {
		k.(*bridge).stopDebug(reason)
		return true
	})
}

// serve handles one request in its own goroutine. While the bridge's debug
// is on, the reply records the request's name and how long it took.
func (b *bridge) serve(req proto.Request) {
	if b.dbg.run.Load() != nil {
		b.dbg.pending.Store(req.ID, pendingRequest{op: req.Op, start: diag.Now()})
	}
	b.handle(req)
}

// noteReply records the request that id answers, before the reply goes,
// when the bridge's debug noted it.
func (b *bridge) noteReply(id uint64) {
	run := b.dbg.run.Load()
	if run == nil {
		return
	}
	if v, ok := b.dbg.pending.LoadAndDelete(id); ok {
		p, _ := v.(pendingRequest)
		end := diag.Now()
		run.File.Add(diag.Record{T: end, Cat: diag.CatNet, Ev: "request", Op: p.op, Dur: end - p.start})
	}
}

// note records a frame that is not a request, such as an attach, while the
// bridge's debug is on.
func (b *bridge) note(op string, n int) {
	if run := b.dbg.run.Load(); run != nil {
		run.File.Add(diag.Record{T: diag.Now(), Cat: diag.CatNet, Ev: "request", Op: op, N: n})
	}
}

// handleDebug serves the debug requests. It reports whether it knew the
// request.
func (b *bridge) handleDebug(req proto.Request) bool {
	switch req.Op {
	case api.OpDebugStart, api.OpDebugStop, api.OpDebugProfile, api.OpDebugSave:
	default:
		return false
	}
	var a struct {
		ID      string `json:"id"`
		Seconds int    `json:"seconds"`
	}
	if len(req.Args) > 0 && json.Unmarshal(req.Args, &a) != nil {
		b.reply(req.ID, nil, badDebugArgs())
		return true
	}
	if a.ID == "" && (req.Op == api.OpDebugStart || req.Op == api.OpDebugStop) {
		v, err := b.ownDebug(req)
		b.reply(req.ID, v, err)
		return true
	}
	if !validID(a.ID) {
		b.reply(req.ID, nil, UserError("not_found", "no session "+a.ID, "List the host's sessions to find its ID."))
		return true
	}
	wait := holderWait
	if req.Op == api.OpDebugProfile {
		d := time.Duration(a.Seconds) * time.Second
		if d <= 0 {
			d = diag.ProfileDefault
		}
		wait += min(d, diag.ProfileMax)
	}
	b.log.Debug("debug request", "op", req.Op, "session", a.ID)
	res, err := HolderRequest(a.ID, req.Op, req.Args, wait)
	b.reply(req.ID, res, err)
	return true
}

// ownDebug starts or stops the bridge's own debug.
func (b *bridge) ownDebug(req proto.Request) (api.DebugState, error) {
	if req.Op == api.OpDebugStop {
		b.stopDebug("request")
		return api.DebugState{}, nil
	}
	var a api.DebugStartArgs
	_ = json.Unmarshal(req.Args, &a)
	cats := diag.CatsOf(a.Categories) & diag.BridgeCats
	b.dbg.mu.Lock()
	defer b.dbg.mu.Unlock()
	run := b.dbg.run.Load()
	switch {
	case cats == 0:
		return api.DebugState{}, nil // nothing that the bridge records
	case run != nil:
		run.SetCats(cats)
	default:
		var err error
		if run, err = b.startDebug(cats); err != nil {
			b.log.Warn("debug not started", "err", err)
			return api.DebugState{}, UserError("debug_file", "cannot write the bridge's debug file: "+err.Error(), "Check the host's disk.")
		}
		b.dbg.run.Store(run)
	}
	return api.DebugState{Categories: cats.List(), File: homeRel(run.File.Path(), "")}, nil
}

// startDebug opens a new debug file for this bridge in the host's debug
// folder: bridge-<time>-<pid>.jsonl. The caller holds b.dbg.mu.
func (b *bridge) startDebug(cats diag.Cats) (*diag.Run, error) {
	dir := paths.DebugDir()
	if err := diag.EnsureDir(dir); err != nil {
		return nil, err
	}
	now := time.Now()
	path := diag.FreePath(dir, fmt.Sprintf("bridge-%s-%d", diag.TimeName(now), os.Getpid()), ".jsonl")
	prefix := strings.TrimSuffix(filepath.Base(path), ".jsonl") + "-"
	run, err := diag.StartRun(cats, diag.RunOptions{
		Path:    path,
		Start:   diag.Record{Side: "bridge", Version: version.Version, Wall: now.UnixNano()},
		Runtime: diag.RuntimeOptions{Dir: dir, Max: diag.FolderMax, Prefix: prefix, CPU: true},
		Lines:   b.dbg.lines,
	})
	if err != nil {
		return nil, fmt.Errorf("start the bridge's debug: %w", err)
	}
	return run, nil
}

// stopDebug ends the bridge's own debug, if it is on.
func (b *bridge) stopDebug(reason string) {
	b.dbg.mu.Lock()
	defer b.dbg.mu.Unlock()
	if run := b.dbg.run.Swap(nil); run != nil {
		if err := run.Stop(reason); err != nil {
			b.log.Warn("debug file not complete", "err", err)
		}
	}
	b.dbg.pending.Clear()
}
