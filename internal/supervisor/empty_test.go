package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

type emptyTestRuntime struct{ clean bool }

func (*emptyTestRuntime) Start(context.Context) (net.Conn, error) { return nil, errors.New("not used") }
func (*emptyTestRuntime) Kill() error                             { return nil }
func (*emptyTestRuntime) Wait() error                             { return nil }
func (r *emptyTestRuntime) CleanExit() bool                       { return r.clean }

func emptyProofFixture() (*Supervisor, *Supervisor) {
	root := &Supervisor{id: "a2a2a2a2", generation: "current", runToken: "current/a2a2a2a2", launched: true, sb: &emptyTestRuntime{true}, m: &host.Manifest{ID: "a2a2a2a2", Guest: &host.GuestClaims{ExitCode: new(1)}}, rec: record{phase: host.LifeRunning}, end: endFacts{claimed: true, token: "current/a2a2a2a2"}}
	child := &Supervisor{id: "b3b3b3b3", generation: "current", runToken: "current/b3b3b3b3", launched: true, primary: root, m: &host.Manifest{ID: "b3b3b3b3", Guest: &host.GuestClaims{ExitCode: new(0)}}, end: endFacts{claimed: true, token: "current/b3b3b3b3"}}
	root.registerLaunch()
	child.registerLaunch()
	return root, child
}
func TestNaturalEmptyNeedsEveryCurrentMemberAndOwnedCleanExit(t *testing.T) {
	tests := []struct {
		name     string
		change   func(*Supervisor, *Supervisor)
		observed bool
		cleanup  error
	}{
		{"no owned wait", func(*Supervisor, *Supervisor) {}, false, nil},
		{"failed cleanup", func(*Supervisor, *Supervisor) {}, true, errors.New("cleanup")},
		{"runtime signal or forced kill", func(r, c *Supervisor) { r.sb = &emptyTestRuntime{false} }, true, nil},
		{"missing child end", func(r, c *Supervisor) { c.end.claimed = false }, true, nil},
		{"stale end", func(r, c *Supervisor) { c.end.token = "previous/b3b3b3b3" }, true, nil},
		{"wrong epoch", func(r, c *Supervisor) { c.generation = "previous" }, true, nil},
		{"unacknowledged launch", func(r, c *Supervisor) { c.launched = false }, true, nil},
		{"unregistered saved member", func(r, c *Supervisor) { r.expected["c4c4c4c4"] = true }, true, nil},
		{"uncertain member launch", func(r, c *Supervisor) { r.memberFault = true }, true, nil},
		{"link failure", func(r, c *Supervisor) { r.lost = "unexpected" }, true, nil},
		{"missed heartbeat", func(r, c *Supervisor) { r.silent = true }, true, nil},
		{"OOM", func(r, c *Supervisor) { r.rec.oom = true }, true, nil},
		{"failed capture", func(r, c *Supervisor) { r.captureFailed = true }, true, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, child := emptyProofFixture()
			test.change(root, child)
			if root.naturalEmpty(test.observed, test.cleanup) {
				t.Fatal("incomplete evidence became a clean empty end")
			}
		})
	}
	root, _ := emptyProofFixture()
	if !root.naturalEmpty(true, nil) {
		t.Fatal("complete owned clean empty end refused")
	}
	root.rec.end = &host.LifeEnd{Kind: host.EndEmpty, At: time.Now()}
	if status := root.endStatus(); status.State != host.StateErrored || status.ExitCode == nil || *status.ExitCode != 1 {
		t.Fatalf("individual nonzero exit lost: %+v", status)
	}
}
func TestEarlyCorrelatedEndCanWaitButCannotProveLaunchOrExit(t *testing.T) {
	root, _ := emptyProofFixture()
	root.launched = false
	root.end = endFacts{}
	event, _ := json.Marshal(proto.CtlEvent{Event: proto.EventEnded, Session: root.id, RunToken: root.runToken, ExitCode: new(0)})
	root.early = [][]byte{event}
	if !root.pendingEmpty() {
		t.Fatal("a complete early end could not await owned runtime exit")
	}
	if root.naturalEmpty(true, nil) {
		t.Fatal("early event manufactured launch acknowledgement")
	}
	root.early = nil
	if root.pendingEmpty() {
		t.Fatal("guest EOF alone implied an empty sandbox")
	}
}
func TestEveryKnownMemberHasOneReservedTerminalEvent(t *testing.T) {
	root, child := emptyProofFixture()
	root.members = map[string]*Supervisor{child.id: child}
	now := time.Now()
	ctl := &ctlConn{s: root, rate: rateLimit{tokens: 0, last: now}}
	for _, member := range []*Supervisor{root, child} {
		p, _ := json.Marshal(proto.CtlEvent{Event: proto.EventEnded, Session: member.id, RunToken: member.runToken, ExitCode: new(0)})
		if !ctl.allow(now, p) {
			t.Fatalf("member%s lost its only end", member.id)
		}
		if ctl.allow(now, p) {
			t.Fatal("duplicate end bypassed quota")
		}
	}
	unknown, _ := json.Marshal(proto.CtlEvent{Event: proto.EventEnded, Session: "c4c4c4c4", ExitCode: new(0)})
	if ctl.allow(now, unknown) {
		t.Fatal("unknown member acquired reserved room")
	}
}
