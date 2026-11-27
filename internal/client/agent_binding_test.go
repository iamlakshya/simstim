package client

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/agentbridge"
)

func TestVerifiedBindingCannotMoveToAnotherOwnerOrLease(t *testing.T) {
	scope := agentbridge.Scope{Session: "abc234de", Lease: strings.Repeat("1", 32)}
	owner := &Conn{done: make(chan struct{})}
	// Deliberately pre-sealed fixture. Native cryptographic provenance has
	// separate tests. This checks that authority remains scoped and live.
	binding := &peerAgentBinding{wire: []byte("public signed fixture"), owner: owner, scope: scope, verified: true}
	if !binding.matches(owner, scope) {
		t.Fatal("fixture lost its exact live owner")
	}
	if binding.matches(&Conn{done: make(chan struct{})}, scope) || binding.matches(nil, scope) {
		t.Fatal("binding moved to another owner")
	}
	other := scope
	other.Session = "different2"
	if binding.matches(owner, other) {
		t.Fatal("binding moved to another session")
	}
	other = scope
	other.Lease = strings.Repeat("2", 32)
	if binding.matches(owner, other) {
		t.Fatal("binding moved to another lease")
	}
	process := &sshProc{rawDone: make(chan struct{})}
	owner.proc = process
	close(process.rawDone)
	if binding.matches(owner, scope) {
		t.Fatal("bounded log finalization extended a dead process's authority")
	}
	close(owner.done)
	if binding.matches(owner, scope) {
		t.Fatal("a closed connection retained credential authority")
	}
}
