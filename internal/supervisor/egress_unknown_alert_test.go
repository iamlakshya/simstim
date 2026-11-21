package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
)

type unknownAuditHandler struct{ failed chan struct{} }

type unknownAlertTransport func(*http.Request) (*http.Response, error)

func (f unknownAlertTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type unknownAlertClock struct{ ticks atomic.Int64 }

func (c *unknownAlertClock) Now() time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(c.ticks.Add(1)) * 2 * time.Minute)
}
func (*unknownAlertClock) AfterFunc(delay time.Duration, run func()) func() bool {
	return time.AfterFunc(delay, run).Stop
}

func (*unknownAuditHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *unknownAuditHandler) Handle(_ context.Context, record slog.Record) error {
	if record.Message == "unknown allowance audit was not accepted" {
		select {
		case h.failed <- struct{}{}:
		default:
		}
	}
	return nil
}
func (h *unknownAuditHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *unknownAuditHandler) WithGroup(string) slog.Handler      { return h }

type unknownAcceptedFixture struct {
	s         *Supervisor
	c         *egressCtl
	server    *egress.Server
	native    chan alerts.Event
	failed    chan struct{}
	auditPath string
	stop      func()
}

// This is the actual egress producer and actual supervisor consumer on owned
// pipes. It is not native process/gVisor sandbox traffic acceptance.
func newUnknownAcceptedFixture(t *testing.T, values map[string]string) *unknownAcceptedFixture {
	t.Helper()
	root := t.TempDir()
	fixture := &unknownAcceptedFixture{native: make(chan alerts.Event, 64), failed: make(chan struct{}, 8), auditPath: filepath.Join(root, "audit.jsonl")}
	logger := slog.New(&unknownAuditHandler{failed: fixture.failed})
	fixture.s = &Supervisor{id: "unknown2", log: logger, lifetime: context.Background(), m: &host.Manifest{Schema: 1, ID: "unknown2", Name: "authored", Agent: host.AgentInfo{Kind: "custom-command"}}}
	fixture.s.audit = openAudit(fixture.auditPath, logger)
	settings := filepath.Join(root, "alerts.toml")
	if err := os.WriteFile(settings, []byte("events = [\"egress_unknown\"]\ndelay = \"0s\"\n[[destination]]\ntype = \"webhook\"\nurl = \"https://alerts.example/owned\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.s.al = alerts.New(alerts.Options{Path: settings, Host: "authored", Dir: root, Env: []string{}, Logger: logger,
		Limiter: alerts.NewFileLimiter(filepath.Join(root, "rate")), Log: &alerts.DeliveryLog{Path: filepath.Join(root, "delivery"), Lock: filepath.Join(root, "delivery-lock")},
		Clock: new(unknownAlertClock),
		HTTP: &http.Client{Transport: unknownAlertTransport(func(request *http.Request) (*http.Response, error) {
			if request.URL.Host != "alerts.example" {
				return nil, errors.New("unexpected alert destination")
			}
			var event alerts.Event
			if err := json.NewDecoder(request.Body).Decode(&event); err != nil {
				return nil, err
			}
			fixture.native <- event
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
		})}})
	var origins sync.WaitGroup
	dialer := &egress.Dialer{Lookup: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("198.51.100.8")}, nil
	},
		DialTCP: func(ctx context.Context, target netip.AddrPort) (net.Conn, error) {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if target != netip.MustParseAddrPort("198.51.100.8:80") {
				return nil, errors.New("unknown fixture reached an unchecked origin")
			}
			client, origin := net.Pipe()
			origins.Go(func() {
				defer origin.Close()
				request, err := http.ReadRequest(bufio.NewReader(origin))
				if err != nil {
					return
				}
				_, _ = io.Copy(io.Discard, request.Body)
				_ = request.Body.Close()
				_, _ = io.WriteString(origin, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
			})
			return client, nil
		}}
	a, b := net.Pipe()
	server, err := egress.NewServer(b, egress.Options{Dialer: dialer, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	fixture.server = server
	served := make(chan struct{})
	go func() { defer close(served); server.Serve() }()
	ep := &egressProc{sess: link.New(a, link.Supervisor, egress.LinkOptions())}
	fixture.s.eg = ep
	stream, err := ep.sess.Open(t.Context(), link.KindCtl, nil)
	if err != nil {
		t.Fatal(err)
	}
	fixture.c = newEgressCtl(t.Context(), fixture.s, stream, ep)
	ep.ctl = fixture.c
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := fixture.c.request(ctx, egress.CtlInit, egress.InitArgs{SessionID: "unknown2", Agent: "shell", DNS: egress.DNSModeReal, Secrets: values, Policy: api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownAllow}}, nil); err != nil {
		t.Fatal(err)
	}
	stopOnce := new(sync.Once)
	fixture.stop = func() {
		stopOnce.Do(func() {
			_ = server.Close()
			_ = ep.sess.Close()
			waitEventSignal(t, served, "actual unknown egress server")
			waitEventSignal(t, fixture.c.done, "actual unknown control reader")
			fixture.s.wg.Wait()
			origins.Wait()
			fixture.s.al.Close(time.Second)
			fixture.s.audit.close()
		})
	}
	t.Cleanup(fixture.stop)
	return fixture
}

func (f *unknownAcceptedFixture) flow(t *testing.T, hostname string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	stream, err := f.s.eg.sess.Open(ctx, link.KindEgress, egress.StreamHeader{Via: "explicit", Host: hostname, Port: 80})
	if err != nil {
		return err
	}
	defer stream.Close()
	if _, err := fmt.Fprintf(stream, "GET http://%s:80/authored HTTP/1.1\r\nHost: %s:80\r\nConnection: close\r\n\r\n", hostname, hostname); err != nil {
		return err
	}
	reader := bufio.NewReader(stream)
	response, err := http.ReadResponse(reader, &http.Request{Method: "GET"})
	if err != nil {
		return err
	}
	_, readErr := io.Copy(io.Discard, response.Body)
	closeErr := response.Body.Close()
	_, eofErr := io.Copy(io.Discard, reader) // full stream completion follows its audit write
	if response.StatusCode != 200 {
		return errors.New("actual unknown fixture did not reach its checked origin")
	}
	return errors.Join(readErr, closeErr, eofErr)
}

func (f *unknownAcceptedFixture) barrier(t *testing.T, hostname string) []alerts.Event {
	t.Helper()
	if err := f.flow(t, hostname); err != nil {
		t.Fatal(err)
	}
	var events []alerts.Event
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-f.native:
			events = append(events, event)
			if event.Event == alerts.EgressUnknown && event.Reason == hostname {
				return events
			}
		case <-timer.C:
			t.Fatal("actual accepted-audit barrier did not publish its native event")
		}
	}
}

func TestUnknownFirstConcurrentAuditsUseActualProducerAppendAndControlElection(t *testing.T) {
	fixture := newUnknownAcceptedFixture(t, nil)
	var flows sync.WaitGroup
	failures := make(chan error, 16)
	for range 16 {
		flows.Go(func() { failures <- fixture.flow(t, "authored.example") })
	}
	flows.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	events := fixture.barrier(t, "barrier.example")
	count := 0
	for _, event := range events {
		if event.Event == alerts.EgressUnknown && event.Reason == "authored.example" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("concurrent actual accepted audits did not raise exactly one first-host event")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := fixture.c.request(ctx, egress.CtlPolicyUpdate, egress.PolicyUpdateArgs{Policy: api.Policy{Preset: api.PresetOpen, Unknown: api.UnknownAllow}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := fixture.flow(t, "authored.example"); err != nil {
		t.Fatal(err)
	}
	for _, event := range fixture.barrier(t, "later-barrier.example") {
		if event.Event == alerts.EgressUnknown && event.Reason == "authored.example" {
			t.Fatal("live policy update repeated the first-host event")
		}
	}
}

func TestUnknownFailedAuditDoesNotSuppressLaterAcceptedAlertOrExposeHostValue(t *testing.T) {
	value := "authoredexposure"
	fixture := newUnknownAcceptedFixture(t, map[string]string{"EXPOSED": value})
	fixture.s.audit.mu.Lock()
	_ = fixture.s.audit.f.Close()
	fixture.s.audit.mu.Unlock()
	if err := fixture.flow(t, value+".example.test"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fixture.failed:
	case <-time.After(5 * time.Second):
		t.Fatal("actual failed audit was not observed")
	}
	fixture.s.audit.mu.Lock()
	file, err := os.OpenFile(fixture.auditPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		fixture.s.audit.f = file
	}
	fixture.s.audit.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.flow(t, value+".example.test"); err != nil {
		t.Fatal(err)
	}
	events := fixture.barrier(t, "barrier.example")
	count := 0
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil || strings.Contains(string(encoded), value) {
			t.Fatal("raw hostname value reached a native public event", err)
		}
		if event.Event == alerts.EgressUnknown && event.Reason == "[EXPOSED].example.test" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("failed audit permanently suppressed the later accepted first-host event")
	}
	data, err := os.ReadFile(fixture.auditPath)
	if err != nil || strings.Contains(string(data), value) || !strings.Contains(string(data), "allow-unknown") {
		t.Fatal("actual audit acceptance leaked a source-known hostname value", err)
	}
}

func TestUnknownColdActorAndStaleOwnerKeepIndependentFirstHostState(t *testing.T) {
	old := newUnknownAcceptedFixture(t, nil)
	if err := old.flow(t, "authored.example"); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range old.barrier(t, "barrier.example") {
		if event.Reason == "authored.example" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("old actor did not accept its first host once")
	}
	old.stop()
	fresh := newUnknownAcceptedFixture(t, nil)
	if err := fresh.flow(t, "authored.example"); err != nil {
		t.Fatal(err)
	}
	count = 0
	for _, event := range fresh.barrier(t, "barrier.example") {
		if event.Reason == "authored.example" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("genuine fresh actor inherited a cold predecessor's delivered flag")
	}

	stale := newUnknownAcceptedFixture(t, nil)
	stale.s.audit.mu.Lock() // hold actual append before its owner check
	if err := stale.flow(t, "stale.example"); err != nil {
		stale.s.audit.mu.Unlock()
		t.Fatal(err)
	}
	stale.s.mu.Lock()
	stale.s.eg = new(egressProc)
	stale.s.mu.Unlock()
	stale.s.audit.mu.Unlock()
	_ = stale.c.st.Close()
	waitEventSignal(t, stale.c.done, "stale unknown control")
	waitEventSignal(t, stale.c.events.finished, "stale unknown audit drain")
	select {
	case <-stale.native:
		t.Fatal("an old audit owner raised an event on a replacement actor")
	default:
	}
}
