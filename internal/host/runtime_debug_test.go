package host_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// hostPath turns a path that the host gave from ~ into a path on this
// machine.
func hostPath(t *testing.T, h *hosttest.Host, p string) string {
	t.Helper()
	rest, ok := strings.CutPrefix(p, "~/")
	if !ok {
		t.Fatalf("the host gave %q, want a path from ~", p)
	}
	return filepath.Join(h.Home, rest)
}

func decode[T any](t *testing.T, r proto.Response) T {
	t.Helper()
	var v T
	if !r.OK {
		t.Fatalf("the request failed: %+v", r.Error)
	}
	if err := json.Unmarshal(r.Result, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func listed(t *testing.T, id string) host.SessionInfo {
	t.Helper()
	list, err := host.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no session %s", id)
	return host.SessionInfo{}
}

// debug start turns on a running session's debug: the holder records the
// keys of an attach that asked for no trace, with an ID of its own, and
// writes profiles and its flight recorder on request. debug stop ends it.
func TestDebugStartAndStopInARunningSession(t *testing.T) {
	h := hosttest.New(t)
	s := start(t, "debugged", "/bin/sh", "-c", "stty raw -echo; printf 'READY'; head -c 4 >/dev/null; printf 'GOT'; exec sleep 60")
	waitOutput(t, s.ID, "READY")
	b := newRawBridge(t)
	b.attach(s.ID, -1, 80, 24)
	b.output(0, "READY")

	st := decode[api.DebugState](t, b.request(1, api.OpDebugStart, api.DebugStartArgs{ID: s.ID, Seconds: 600}))
	if strings.Join(st.Categories, ",") != "attach,memory,runtime" || st.Until == nil || time.Until(*st.Until) < 9*time.Minute {
		t.Fatalf("debug start = %+v", st)
	}
	if got := hostPath(t, h, st.File); got != paths.DebugTrace(s.ID) {
		t.Fatalf("the debug file is %s, want %s", got, paths.DebugTrace(s.ID))
	}
	hosttest.WaitFor(t, 5*time.Second, "ls to show the end", func() bool { return listed(t, s.ID).DebugUntil != nil })

	b.send(proto.TInput, []byte("KEYS"))
	b.output(5, "GOT")

	saved := decode[api.DebugFile](t, b.request(2, api.OpDebugSave, map[string]string{"id": s.ID}))
	if p := hostPath(t, h, saved.Path); filepath.Dir(p) != paths.SessionDebugDir(s.ID) || !strings.HasPrefix(filepath.Base(p), "flight-") || saved.Size == 0 {
		t.Fatalf("debug save = %+v", saved)
	}
	t.Logf("the flight recorder's window of an idle holder: %d bytes", saved.Size)
	heap := decode[api.DebugFile](t, b.request(3, api.OpDebugProfile, api.DebugProfileArgs{ID: s.ID, Kind: "heap"}))
	if p := hostPath(t, h, heap.Path); !strings.HasPrefix(filepath.Base(p), "heap-") || heap.Size == 0 {
		t.Fatalf("debug profile heap = %+v", heap)
	}
	for _, p := range []string{hostPath(t, h, saved.Path), hostPath(t, h, heap.Path), paths.DebugTrace(s.ID)} {
		if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v, %v; want mode 600", p, st, err)
		}
	}
	if st, err := os.Stat(paths.SessionDebugDir(s.ID)); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("the session's debug folder: %v, %v; want mode 700", st, err)
	}

	stopped := b.request(4, api.OpDebugStop, map[string]string{"id": s.ID})
	if !stopped.OK {
		t.Fatalf("debug stop = %+v", stopped.Error)
	}
	hosttest.WaitFor(t, 10*time.Second, "the trace to stop", func() bool {
		return strings.Contains(h.HolderLog(s.ID), "debug trace off")
	})
	hosttest.WaitFor(t, 5*time.Second, "ls to show no end", func() bool { return listed(t, s.ID).DebugUntil == nil })
	recs := records(t, mustRead(t, paths.DebugTrace(s.ID)))
	var key *diag.Record
	for i, r := range recs {
		if r.Ev == "key" {
			key = &recs[i]
		}
	}
	if key == nil || !strings.HasPrefix(key.Trace, "h") || key.Key != 1 || key.N != 4 {
		t.Errorf("the untraced attach's key = %+v, want one with the holder's ID", key)
	}
	if !slices.ContainsFunc(recs, func(r diag.Record) bool { return r.Ev == diag.EvSave && r.Reason == "request" }) {
		t.Error("the trace has no save record for the profile")
	}
	if last := recs[len(recs)-1]; last.Ev != diag.EvStop || last.Reason != "request" {
		t.Errorf("the last record = %+v, want the stop", last)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Debug stops by itself at the end of its time, and a save with the
// runtime category off says how to turn it on.
func TestDebugStopsByItselfAndSaveNeedsTheRecorder(t *testing.T) {
	h := hosttest.New(t)
	s := start(t, "brief", "/bin/sh", "-c", "exec sleep 60")
	b := newRawBridge(t)
	st := decode[api.DebugState](t, b.request(1, api.OpDebugStart, api.DebugStartArgs{ID: s.ID, Categories: []string{"memory", "net"}, Seconds: 1}))
	if strings.Join(st.Categories, ",") != "memory" {
		t.Fatalf("the host records %v, want memory alone", st.Categories)
	}
	r := b.request(2, api.OpDebugSave, map[string]string{"id": s.ID})
	if r.OK || r.Error.Code != "no_recorder" || !strings.Contains(r.Error.Hint, "debug start") {
		t.Fatalf("a save without the recorder = %+v", r.Error)
	}
	hosttest.WaitFor(t, 10*time.Second, "debug to stop by itself", func() bool {
		return strings.Contains(h.HolderLog(s.ID), `"debug stop" reason=time`)
	})
	hosttest.WaitFor(t, 5*time.Second, "ls to show no end", func() bool { return listed(t, s.ID).DebugUntil == nil })
	r = b.request(3, api.OpDebugStart, api.DebugStartArgs{ID: s.ID, Categories: []string{"net"}})
	if r.OK || r.Error.Code != "bad_request" {
		t.Fatalf("a start with no host category = %+v", r)
	}
	r = b.request(4, api.OpDebugProfile, api.DebugProfileArgs{ID: "nosuch1", Kind: "heap"})
	if r.OK || r.Error.Code != "not_found" {
		t.Fatalf("a profile of no session = %+v", r)
	}
}

// A second profile while one runs gets a busy error, which says to wait.
func TestASecondProfileIsBusy(t *testing.T) {
	h := hosttest.New(t)
	s := start(t, "busy", "/bin/sh", "-c", "exec sleep 60")
	b := newRawBridge(t)
	b.sendJSON(proto.TRequest, proto.Request{ID: 1, Op: api.OpDebugProfile, Args: mustJSON(t, api.DebugProfileArgs{ID: s.ID, Kind: "cpu", Seconds: 3})})
	hosttest.WaitFor(t, 10*time.Second, "the CPU profile to start", func() bool {
		return strings.Contains(h.HolderLog(s.ID), `msg="writing a profile" kind=cpu`)
	})
	r := b.request(2, api.OpDebugProfile, api.DebugProfileArgs{ID: s.ID, Kind: "goroutine"})
	if r.OK || r.Error.Code != api.CodeBusy || !strings.Contains(r.Error.Hint, "Wait") {
		t.Fatalf("a second profile = %+v", r)
	}
	cpu := decode[api.DebugFile](t, b.response(1))
	if !strings.Contains(cpu.Path, "/debug/cpu-") || cpu.Size == 0 {
		t.Fatalf("the CPU profile = %+v", cpu)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A client whose debug is on asks its bridge to record: each request and
// its time, each attach and detach, into a file of its own in the host's
// debug folder, until debug.stop.
func TestTheBridgeRecordsItsRequests(t *testing.T) {
	h := hosttest.New(t)
	b := newRawBridge(t)
	st := decode[api.DebugState](t, b.request(1, api.OpDebugStart, api.DebugStartArgs{Categories: []string{"attach", "net"}}))
	if strings.Join(st.Categories, ",") != "net" {
		t.Fatalf("the bridge records %v, want net", st.Categories)
	}
	path := hostPath(t, h, st.File)
	if filepath.Dir(path) != paths.DebugDir() || !strings.HasPrefix(filepath.Base(path), "bridge-") {
		t.Fatalf("the bridge's file is %s", path)
	}
	b.request(2, "ls", nil)
	b.send(proto.TDetach, nil)
	b.request(3, api.OpDebugStop, nil)
	recs := records(t, mustRead(t, path))
	ops := map[string]bool{}
	for _, r := range recs {
		if r.Ev == "request" {
			ops[r.Op] = true
		}
	}
	if !ops["ls"] || !ops["detach"] || recs[0].Ev != diag.EvStart || recs[0].Side != "bridge" || recs[len(recs)-1].Ev != diag.EvStop {
		t.Fatalf("records = %+v", recs)
	}
	// A start with no category of the bridge records nothing.
	r := decode[api.DebugState](t, b.request(4, api.OpDebugStart, api.DebugStartArgs{Categories: []string{"launcher"}}))
	if r.File != "" {
		t.Fatalf("a start with the launcher alone = %+v", r)
	}
}

// A resume starts a new holder, which starts with debug off, as after a
// reboot. The old holder writes its stop record, ls shows no end, and the
// resume says that debug stopped.
func TestDebugStopsAtAResume(t *testing.T) {
	h := hosttest.New(t)
	fakeClaude(t, h, `[ -e "$HOME/ran" ] && exec sleep 60; touch "$HOME/ran"; exit 3`)
	s, err := host.New(host.NewArgs{Agent: "claude", Name: "app", Cwd: "~"})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, s.ID, host.StateErrored)
	if _, err := host.HolderRequest(s.ID, api.OpDebugStart, api.DebugStartArgs{ID: s.ID, Categories: []string{"memory"}}, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if listed(t, s.ID).DebugUntil == nil {
		t.Fatal("ls shows no end while debug is on")
	}
	r := resume(t, api.ResumeArgs{ID: s.ID})
	if !r.DebugStopped || r.Session.DebugUntil != nil {
		t.Fatalf("resume = %+v, want debug stopped", r)
	}
	recs := records(t, mustRead(t, paths.DebugTrace(s.ID)))
	if last := recs[len(recs)-1]; last.Ev != diag.EvStop || last.Reason != "exit" {
		t.Errorf("the old holder's last record = %+v, want its stop", last)
	}
	raw, err := host.HolderRequest(s.ID, api.OpDebugStop, map[string]string{"id": s.ID}, 5*time.Second)
	var st api.DebugState
	if err != nil || json.Unmarshal(raw, &st) != nil || st.Stopped {
		t.Fatalf("the new holder's debug stop = %s, %v; want nothing to stop", raw, err)
	}
}
