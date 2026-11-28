package supervisor_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supervisor"
	"github.com/iamlakshya/simstim/internal/version"
)

func TestMaxRunStopsSandboxAndDeliversOTLPAlert(t *testing.T) {
	hosttest.New(t)

	var mu sync.Mutex
	var events []string
	var reasons []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			ResourceLogs []struct {
				ScopeLogs []struct {
					LogRecords []struct {
						Attributes []struct {
							Key   string `json:"key"`
							Value struct {
								StringValue string `json:"stringValue"`
							} `json:"value"`
						} `json:"attributes"`
					} `json:"logRecords"`
				} `json:"scopeLogs"`
			} `json:"resourceLogs"`
		}
		if json.Unmarshal(body, &payload) == nil {
			mu.Lock()
			for _, rl := range payload.ResourceLogs {
				for _, sl := range rl.ScopeLogs {
					for _, lr := range sl.LogRecords {
						for _, attr := range lr.Attributes {
							switch attr.Key {
							case brand.Name + ".event":
								events = append(events, attr.Value.StringValue)
							case brand.Name + ".reason":
								reasons = append(reasons, attr.Value.StringValue)
							}
						}
					}
				}
			}
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	alertsPath := filepath.Join(t.TempDir(), "alerts.toml")
	cfg := api.AlertsConfig{
		Events: []string{alerts.SandboxReady, alerts.SandboxStopped, alerts.SandboxError},
		Destinations: []api.AlertDestination{
			{Type: "webhook", URL: srv.URL, Format: "otlp"},
		},
	}
	if err := alerts.Save(alertsPath, cfg); err != nil {
		t.Fatalf("alerts.Save: %v", err)
	}

	id := "maxr2345"
	seedWalledManifest(t, id, "shell")
	sb := newFakeSandbox(t)
	done := runSupervisorBG(t, id, sb, supervisor.Options{
		AlertsPath: alertsPath,
		MaxRun:     350 * time.Millisecond,
	})
	startFakeGuestLink(t, sb)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("supervisor exited with error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("sandbox did not stop on max_run within 10s")
	}

	loaded, err := host.LoadManifest(id)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if loaded.Status.State != host.StateStopped || loaded.Status.Reason != "max_run" {
		t.Fatalf("manifest status = %+v, want stopped/max_run", loaded.Status)
	}

	// Verify OTLP collector received sandbox_ready and sandbox_stopped (reason=max_run).
	hosttest.WaitFor(t, 5*time.Second, "OTLP sandbox_ready and sandbox_stopped alerts", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return slices.Contains(events, alerts.SandboxReady) &&
			slices.Contains(events, alerts.SandboxStopped) &&
			slices.Contains(reasons, "max_run")
	})

	// Verify events.jsonl recorded sandbox_ready and sandbox_stopped.
	evs, err := host.ReadWatchEvents(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	sawReady, sawStopped := false, false
	for _, ev := range evs {
		if ev.Event == api.EventSandboxReady {
			sawReady = true
		}
		if ev.Event == api.EventSandboxStopped && ev.Reason == "max_run" {
			sawStopped = true
		}
	}
	if !sawReady || !sawStopped {
		t.Fatalf("watch events = %+v, want sandbox_ready and sandbox_stopped(max_run)", evs)
	}
}

func TestGuestNeedsInputToEventsWatchLatency(t *testing.T) {
	hosttest.New(t)

	id := "evnt2345"
	seedWalledManifest(t, id, "claude")
	sb := newFakeSandbox(t)
	runSupervisorBG(t, id, sb, supervisor.Options{})
	_, ctlW := startFakeGuestLink(t, sb)

	// Open a live events.watch stream over RunBridge.
	cliConn, srvConn := net.Pipe()
	bridgeDone := make(chan struct{})
	go func() {
		defer close(bridgeDone)
		_ = host.RunBridge(srvConn, srvConn)
		_ = srvConn.Close()
	}()
	defer func() {
		_ = cliConn.Close()
		<-bridgeDone
	}()

	_ = cliConn.SetDeadline(time.Now().Add(5 * time.Second))
	bw := proto.NewWriter(cliConn)
	br := bufio.NewReaderSize(cliConn, 64<<10)
	_ = bw.WriteJSON(proto.THello, proto.Hello{
		Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "client",
	})
	_, _ = proto.Read(br)
	rawArgs, _ := json.Marshal(api.EventsWatchArgs{Session: id, Follow: true})
	_ = bw.WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: "events.watch", Args: rawArgs})
	_, _ = proto.Read(br)
	_ = cliConn.SetDeadline(time.Time{})

	lnk := link.New(&supBufferedConn{Conn: cliConn, br: br}, link.Guest, link.Options{
		Opener:               link.BridgeOpener,
		AllowVersionMismatch: true,
		MaxSupervisorStreams: 4,
		MaxGuestStreams:      1,
	})
	defer lnk.Close()
	actx, acancel := context.WithTimeout(context.Background(), 5*time.Second)
	st, err := lnk.Accept(actx)
	acancel()
	if err != nil {
		t.Fatalf("accept events stream: %v", err)
	}
	defer st.Close()

	time.Sleep(40 * time.Millisecond)

	const samples = 9
	evCh := make(chan api.WatchEvent, samples*4)
	scanErrCh := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(st)
		for sc.Scan() {
			var ev api.WatchEvent
			if json.Unmarshal(sc.Bytes(), &ev) == nil && ev.Event == api.EventNeedsInput {
				evCh <- ev
			}
		}
		scanErrCh <- sc.Err()
	}()

	latencies := make([]time.Duration, 0, samples)
	for i := range samples {
		sentAt := time.Now()
		if err := ctlW.WriteJSON(proto.TEvent, guest.Event{
			Type:    "alert",
			Session: id,
			Kind:    alerts.NeedsInput,
			Detail:  "Approve tool?",
		}); err != nil {
			t.Fatalf("write guest alert: %v", err)
		}
		select {
		case ev := <-evCh:
			latencies = append(latencies, time.Since(sentAt))
			if ev.Detail != "Approve tool?" {
				t.Fatalf("sample %d: detail = %q, want %q", i, ev.Detail, "Approve tool?")
			}
		case err := <-scanErrCh:
			t.Fatalf("sample %d: stream ended before needs_input (err=%v)", i, err)
		case <-time.After(3 * time.Second):
			t.Fatalf("sample %d: timed out waiting for needs_input", i)
		}
		time.Sleep(10 * time.Millisecond)
	}
	slices.Sort(latencies)
	p50 := latencies[len(latencies)/2]
	if p50 > 200*time.Millisecond {
		t.Fatalf("guest needs_input -> events.watch p50 = %v, want <= 200ms (all: %v)", p50, latencies)
	}
}

type supBufferedConn struct {
	net.Conn
	br *bufio.Reader
}

func (c *supBufferedConn) Read(p []byte) (int, error) { return c.br.Read(p) }

// RemoveWaitAttach prevents a fast-exiting walled `new --rm` session from
// tearing down before the initial attach connects, while detached `new --rm`
// (RemoveWaitAttach=false) still removes itself automatically on guest exit.
func TestRemoveWaitAttachDefersAutoStopUntilAttach(t *testing.T) {
	t.Run("wait_attach_keeps_session_before_first_attach", func(t *testing.T) {
		hosttest.New(t)
		id := "rmwt2345"
		seedWalledManifest(t, id, "shell")
		m, err := host.LoadManifest(id)
		if err != nil {
			t.Fatal(err)
		}
		m.RemoveOnExit = true
		m.RemoveWaitAttach = true
		if err := m.Save(); err != nil {
			t.Fatal(err)
		}

		sb := newFakeSandbox(t)
		done := runSupervisorBG(t, id, sb, supervisor.Options{StopGrace: 200 * time.Millisecond})
		_, ctlW := startFakeGuestLink(t, sb)

		if err := ctlW.WriteJSON(proto.TEvent, guest.Event{Type: "ended", Session: id, ExitCode: 7}); err != nil {
			t.Fatalf("write ended: %v", err)
		}
		// Give the supervisor time to process the ended event; it must stay alive
		// and preserve the session directory so the initial attach can collect it.
		select {
		case err := <-done:
			t.Fatalf("supervisor stopped early before first attach: err=%v", err)
		case <-time.After(200 * time.Millisecond):
		}
		if _, err := os.Stat(paths.SessionDir(id)); err != nil {
			t.Fatalf("session dir deleted before first attach: %v", err)
		}
		_ = sb.killWith(supervisor.Exit{Code: 0})
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("supervisor did not exit after sandbox exit")
		}
	})

	t.Run("detached_remove_on_exit_deletes_immediately", func(t *testing.T) {
		hosttest.New(t)
		id := "rmdt2345"
		seedWalledManifest(t, id, "shell")
		m, err := host.LoadManifest(id)
		if err != nil {
			t.Fatal(err)
		}
		m.RemoveOnExit = true
		m.RemoveWaitAttach = false
		if err := m.Save(); err != nil {
			t.Fatal(err)
		}

		sb := newFakeSandbox(t)
		done := runSupervisorBG(t, id, sb, supervisor.Options{StopGrace: 200 * time.Millisecond})
		_, ctlW := startFakeGuestLink(t, sb)

		if err := ctlW.WriteJSON(proto.TEvent, guest.Event{Type: "ended", Session: id, ExitCode: 0}); err != nil {
			t.Fatalf("write ended: %v", err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("supervisor exit: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("supervisor did not stop on detached remove_on_exit")
		}
		if _, err := os.Stat(paths.SessionDir(id)); !os.IsNotExist(err) {
			t.Fatalf("session dir still exists after detached remove_on_exit: %v", err)
		}
	})
}

func TestHostStopWalledSandboxEmitsSandboxStopped(t *testing.T) {
	hosttest.New(t)

	var mu sync.Mutex
	var events []string
	var reasons []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			ResourceLogs []struct {
				ScopeLogs []struct {
					LogRecords []struct {
						Attributes []struct {
							Key   string `json:"key"`
							Value struct {
								StringValue string `json:"stringValue"`
							} `json:"value"`
						} `json:"attributes"`
					} `json:"logRecords"`
				} `json:"scopeLogs"`
			} `json:"resourceLogs"`
		}
		if json.Unmarshal(body, &payload) == nil {
			mu.Lock()
			for _, rl := range payload.ResourceLogs {
				for _, sl := range rl.ScopeLogs {
					for _, lr := range sl.LogRecords {
						for _, attr := range lr.Attributes {
							switch attr.Key {
							case brand.Name + ".event":
								events = append(events, attr.Value.StringValue)
							case brand.Name + ".reason":
								reasons = append(reasons, attr.Value.StringValue)
							}
						}
					}
				}
			}
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	alertsPath := filepath.Join(t.TempDir(), "alerts.toml")
	cfg := api.AlertsConfig{
		Events: []string{alerts.SandboxReady, alerts.SandboxStopped},
		Destinations: []api.AlertDestination{
			{Type: "webhook", URL: srv.URL, Format: "otlp"},
		},
	}
	if err := alerts.Save(alertsPath, cfg); err != nil {
		t.Fatalf("alerts.Save: %v", err)
	}

	id := "stpw2345"
	seedWalledManifest(t, id, "shell")
	sb := newFakeSandbox(t)
	done := runSupervisorBG(t, id, sb, supervisor.Options{
		AlertsPath: alertsPath,
		StopGrace:  200 * time.Millisecond,
	})
	startFakeGuestLink(t, sb)

	res, err := host.Stop(api.StopArgs{ID: id})
	if err != nil {
		t.Fatalf("host.Stop(walled): %v", err)
	}
	if res.AlreadyStopped || res.Session.State != host.StateStopped {
		t.Fatalf("host.Stop result = %+v, want stopped", res)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("supervisor exit: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("supervisor did not exit after host.Stop")
	}

	// Verify OTLP alert received sandbox_stopped with reason="stop".
	hosttest.WaitFor(t, 5*time.Second, "OTLP sandbox_stopped alert on host.Stop", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return slices.Contains(events, alerts.SandboxStopped) && slices.Contains(reasons, "stop")
	})

	// Verify events.jsonl recorded sandbox_stopped with reason="stop" exactly once.
	evs, err := host.ReadWatchEvents(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	stoppedCount := 0
	for _, ev := range evs {
		if ev.Event == api.EventSandboxStopped && ev.Reason == "stop" {
			stoppedCount++
		}
	}
	if stoppedCount != 1 {
		t.Fatalf("sandbox_stopped(stop) count = %d, want 1; events = %+v", stoppedCount, evs)
	}
}
