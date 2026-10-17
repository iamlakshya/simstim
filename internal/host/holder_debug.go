package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// The session's debug, which `debug start` turns on in a running session
// (decision 0025). It records the host categories that it names, for every
// attached client, until `debug stop` or its time limit. The holder also
// writes profiles and saves its flight recorder's window on request.

// debugRequest serves the holder's debug requests. It reports whether it
// knew the request.
func (h *Holder) debugRequest(req proto.Request) (v any, perr *proto.Error, ok bool) {
	switch req.Op {
	case api.OpDebugStart:
		var a api.DebugStartArgs
		if len(req.Args) > 0 && json.Unmarshal(req.Args, &a) != nil {
			return nil, badDebugArgs(), true
		}
		v, perr = h.startDebug(a)
	case api.OpDebugStop:
		v, perr = h.stopDebug("request"), nil
	case api.OpDebugProfile:
		var a api.DebugProfileArgs
		if json.Unmarshal(req.Args, &a) != nil {
			return nil, badDebugArgs(), true
		}
		v, perr = h.profile(a)
	case api.OpDebugSave:
		v, perr = h.saveFlight()
	default:
		return nil, nil, false
	}
	return v, perr, true
}

func badDebugArgs() *proto.Error {
	return &proto.Error{Code: "bad_request", Message: "the debug request is not valid JSON", Hint: "Update the client."}
}

// startDebug turns on the session's debug, with the host categories that a
// names, until its time limit. A second start sets a new end.
func (h *Holder) startDebug(a api.DebugStartArgs) (api.DebugState, *proto.Error) {
	cats := diag.CatsOf(a.Categories) & diag.HostCats
	if len(a.Categories) == 0 {
		cats = diag.HostCats
	}
	if cats == 0 {
		return api.DebugState{}, &proto.Error{Code: "bad_request", Message: "no category of the host: " + strings.Join(a.Categories, ", "),
			Hint: "Name attach, memory or runtime."}
	}
	d := time.Duration(a.Seconds) * time.Second
	if d <= 0 {
		d = api.DebugFor
	}
	d = min(d, api.DebugForMax)
	until := time.Now().Add(d)
	u := until.UTC()
	h.dbg.mu.Lock()
	defer h.dbg.mu.Unlock()
	if h.dbg.ended {
		// The holder exits, as for a resume: a trace now would get no stop.
		return api.DebugState{}, &proto.Error{Code: "exiting", Message: "the session's holder is stopping, so its debug cannot start",
			Hint: "Wait for the session to start again, then run `" + brand.Name + " debug start` again."}
	}
	h.dbg.session, h.dbg.until = cats, until
	if h.dbg.timer != nil {
		h.dbg.timer.Stop()
	}
	h.dbg.timer = time.AfterFunc(d, func() { h.endDebug("time", false) })
	if h.applyDebug("time") == nil {
		h.dbg.session, h.dbg.until = 0, time.Time{}
		h.dbg.timer.Stop()
		return api.DebugState{}, &proto.Error{Code: "debug_file", Message: "cannot write the session's debug file",
			Hint: "Run `" + brand.Name + " host doctor` to check the host's disk."}
	}
	// Under h.dbg.mu, so that a stop that comes next writes its own end
	// after this one.
	h.setDebugUntil(&u)
	h.log.Info("debug start", "cats", cats.String(), "until", u)
	return api.DebugState{Categories: cats.List(), Until: &u, File: homeRel(paths.DebugTrace(h.id), "")}, nil
}

// stopDebug ends the session's debug at once, the host side of each traced
// attach too. Stopped says whether anything was on.
func (h *Holder) stopDebug(reason string) api.DebugState {
	return api.DebugState{Stopped: h.endDebug(reason, true), File: homeRel(paths.DebugTrace(h.id), "")}
}

// endDebug ends the session's debug. When all is set, it also ends the
// host side of each traced attach. At its time limit, it ends only a debug
// whose end has come: a later start may have moved it. It reports whether
// it ended anything.
func (h *Holder) endDebug(reason string, all bool) bool {
	h.dbg.mu.Lock()
	defer h.dbg.mu.Unlock()
	if reason == "time" && time.Now().Before(h.dbg.until) {
		return false
	}
	was := h.dbg.session != 0
	traced := all && len(h.dbg.traced) > 0
	h.dbg.session, h.dbg.until = 0, time.Time{}
	if h.dbg.timer != nil {
		h.dbg.timer.Stop()
		h.dbg.timer = nil
	}
	if all {
		clear(h.dbg.traced)
	}
	h.applyDebug(reason)
	if was {
		h.setDebugUntil(nil)
		h.log.Info("debug stop", "reason", reason)
	}
	return was || traced
}

// setDebugUntil records the end of the session's debug in its manifest at
// once, for `ls --json`. The caller holds h.dbg.mu.
func (h *Holder) setDebugUntil(until *time.Time) {
	h.mu.Lock()
	h.m.DebugUntil = until
	m := *h.m
	h.mu.Unlock()
	if err := h.save(&m, false); err != nil {
		h.log.Warn("manifest not saved", "err", err)
	}
}

// claim marks a profile or a save as running, or fails when one runs.
func (h *Holder) claim() *proto.Error {
	if h.dbg.busy.CompareAndSwap(false, true) {
		return nil
	}
	return &proto.Error{Code: api.CodeBusy, Message: "a profile or a save of this session is already running",
		Hint: "Wait for the first one to finish, then try again."}
}

// profile writes a profile of the holder into the session's debug folder.
// A CPU profile runs for a's time: ProfileDefault, up to ProfileMax. If the
// client stops waiting, the holder still writes the file.
func (h *Holder) profile(a api.DebugProfileArgs) (api.DebugFile, *proto.Error) {
	switch a.Kind {
	case diag.ProfileCPU, diag.ProfileHeap, diag.ProfileGoroutine:
	default:
		return api.DebugFile{}, &proto.Error{Code: "bad_request", Message: "no profile " + a.Kind, Hint: "Name cpu, heap or goroutine."}
	}
	if perr := h.claim(); perr != nil {
		return api.DebugFile{}, perr
	}
	defer h.dbg.busy.Store(false)
	d := time.Duration(a.Seconds) * time.Second
	if d <= 0 {
		d = diag.ProfileDefault
	}
	d = min(d, diag.ProfileMax)
	dir := paths.SessionDebugDir(h.id)
	if a.Kind == diag.ProfileCPU {
		h.log.Info("writing a profile", "kind", a.Kind, "seconds", int(d/time.Second))
	} else {
		h.log.Info("writing a profile", "kind", a.Kind) // taken at once
	}
	path, err := diag.WriteProfile(context.Background(), a.Kind, d, dir)
	if err != nil {
		h.log.Warn("profile not written", "kind", a.Kind, "err", err)
		return api.DebugFile{}, &proto.Error{Code: "profile", Message: "cannot write the profile: " + err.Error(),
			Hint: "Run `" + brand.Name + " host doctor` to check the host's disk."}
	}
	diag.Prune(dir, diag.SessionFolderMax)
	return h.wrote(path, "request"), nil
}

// saveFlight saves the window of the holder's flight recorder, which runs
// while the session's runtime category is on.
func (h *Holder) saveFlight() (api.DebugFile, *proto.Error) {
	if perr := h.claim(); perr != nil {
		return api.DebugFile{}, perr
	}
	defer h.dbg.busy.Store(false)
	var rt *diag.Runtime
	if tr := h.dbg.tr.Load(); tr != nil && tr.run != nil {
		rt = tr.run.Runtime()
	}
	path, err := rt.Save("request")
	switch {
	case errors.Is(err, diag.ErrNoRecorder):
		return api.DebugFile{}, &proto.Error{Code: "no_recorder", Message: "the flight recorder of this session is off: it runs while the runtime category is on",
			Hint: brand.Name + " debug start <host>/<session>   (turns it on)"}
	case errors.Is(err, diag.ErrBusy):
		return api.DebugFile{}, h.claimBusy()
	case err != nil:
		h.log.Warn("flight recorder not saved", "err", err)
		return api.DebugFile{}, &proto.Error{Code: "save", Message: "cannot save the flight recorder's window: " + err.Error(),
			Hint: "Run `" + brand.Name + " host doctor` to check the host's disk."}
	}
	return h.wrote(path, ""), nil
}

// claimBusy is the busy error, for a save that met an automatic one.
func (h *Holder) claimBusy() *proto.Error {
	return &proto.Error{Code: api.CodeBusy, Message: "the flight recorder is saving its window already",
		Hint: "Wait a few seconds, then try again."}
}

// wrote describes a file the holder wrote, and records it in the trace
// when reason is set and the trace runs.
func (h *Holder) wrote(path, reason string) api.DebugFile {
	var size int64
	if st, err := os.Stat(path); err == nil {
		size = st.Size()
	}
	if tr := h.dbg.tr.Load(); tr != nil && reason != "" {
		tr.f.Add(diag.Record{T: diag.Now(), Cat: diag.CatRuntime, Ev: diag.EvSave, File: filepath.Base(path), Reason: reason, N: int(size)})
	}
	h.log.Info("debug file written", "file", filepath.Base(path), "size", size)
	return api.DebugFile{Path: homeRel(path, ""), Size: size}
}

// endAllDebug closes the trace as the holder exits.
func (h *Holder) endAllDebug() {
	h.dbg.mu.Lock()
	defer h.dbg.mu.Unlock()
	h.dbg.ended = true
	h.dbg.session = 0
	if h.dbg.timer != nil {
		h.dbg.timer.Stop()
	}
	clear(h.dbg.traced)
	h.applyDebug("exit")
	// The holder's last save follows: a new holder, after a resume or a
	// restore, starts with debug off, so ls shows no end meanwhile.
	h.mu.Lock()
	h.m.DebugUntil = nil
	h.mu.Unlock()
}
