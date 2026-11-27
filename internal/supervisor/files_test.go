//go:build linux || darwin

package supervisor

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

type captureRuntime struct{ clean bool }

func (captureRuntime) Start(context.Context) (net.Conn, error) { return nil, nil }
func (captureRuntime) Kill() error                             { return nil }
func (captureRuntime) Wait() error                             { return nil }
func (r captureRuntime) CleanExit() bool                       { return r.clean }

func captureSupervisor(t *testing.T, clean bool) (*Supervisor, string) {
	t.Helper()
	session := t.TempDir()
	c, err := files.BeginCapture(session, files.StatePlan{Files: []string{".agent/history"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(c.Incoming, ".agent"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.Incoming, ".agent/history"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	left, right := net.Pipe()
	peer := link.New(right, link.Guest, link.Options{})
	supervisor := link.New(left, link.Supervisor, link.Options{})
	t.Cleanup(func() { _ = supervisor.Close(); _ = peer.Close(); _ = c.Close() })
	return &Supervisor{id: "capture2", capture: c, sb: captureRuntime{clean: clean}, link: supervisor, log: slog.New(slog.NewTextHandler(io.Discard, nil))}, session
}

func TestStatePublicationNeedsReadyAndTrustedCleanLifetime(t *testing.T) {
	for _, test := range []struct {
		name, kind                                  string
		clean, natural, ready, failed, broken, want bool
	}{
		{name: "ready alone", kind: host.EndStop, ready: true},
		{name: "exit alone", kind: host.EndStop, clean: true},
		{name: "forced kill", kind: host.EndKill, clean: true, ready: true},
		{name: "failed boot cannot impersonate natural exit", kind: host.EndBoot, clean: true, natural: true, ready: true},
		{name: "broken link", kind: host.EndStop, clean: true, ready: true, broken: true},
		{name: "earlier missed heartbeat", kind: host.EndStop, clean: true, ready: true, failed: true},
		{name: "clean stop", kind: host.EndStop, clean: true, ready: true, want: true},
		{name: "natural clean exit", kind: host.EndSandbox, clean: true, natural: true, ready: true, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, session := captureSupervisor(t, test.clean)
			s.readyCapture("../state-files/CURRENT")
			if s.captureReady != "" {
				t.Fatal("guest selected a commit path")
			}
			if test.ready {
				s.readyCapture(s.capture.Nonce)
			}
			s.captureFailed = test.failed
			if test.broken {
				s.lost = "fixture link failure"
			}
			s.commitCapture(test.kind, test.natural)
			_, err := os.Stat(filepath.Join(session, "state-files/CURRENT"))
			if test.want && err != nil {
				t.Fatal(err)
			}
			if !test.want && !os.IsNotExist(err) {
				t.Fatal("unproved capture published", err)
			}
		})
	}
}

func TestOnlyOneMatchingCaptureReadyBypassesEventQuota(t *testing.T) {
	s, _ := captureSupervisor(t, true)
	c := &ctlConn{s: s}
	now := time.Now()
	event, _ := json.Marshal(proto.CtlEvent{Event: proto.EventStateReady, Session: s.id, CaptureNonce: s.capture.Nonce})
	if !c.allow(now, event) {
		t.Fatal("first capture-ready was dropped")
	}
	passed := 0
	for range 1000 {
		if c.allow(now, event) {
			passed++
		}
	}
	if passed > eventRate {
		t.Fatalf("capture-ready bypassed quota %d times", passed)
	}
	forged, _ := json.Marshal(proto.CtlEvent{Event: proto.EventStateReady, Session: s.id, CaptureNonce: "../CURRENT"})
	if s.validCaptureMessage(forged) {
		t.Fatal("forged state path accepted as nonce")
	}
}
