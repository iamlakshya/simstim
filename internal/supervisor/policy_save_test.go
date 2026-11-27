package supervisor

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
)

func TestPolicySaveFailureRevokesWithoutJoiningUnderPolicyTurn(t *testing.T) {
	fixture := eventController(t)
	fixture.s.mu.Lock()
	fixture.s.m.Armed = true
	fixture.s.mu.Unlock()
	previous := saveManifest
	saveManifest = func(*host.Manifest) error { return errors.New("injected manifest failure") }
	t.Cleanup(func() { saveManifest = previous })
	finished := make(chan error, 1)
	go func() {
		_, err := fixture.s.applyLivePolicyAdd(api.PolicyAddArgs{Allow: true, Target: "example.com", Once: true})
		if err != nil {
			finished <- err
		} else {
			finished <- nil
		}
	}()
	request := fixture.next(t)
	var update egress.PolicyUpdateArgs
	if err := json.Unmarshal(request.Args, &update); err != nil || len(update.Policy.Rules) != 1 {
		t.Fatalf("policy update = %+v, %v", update, err)
	}
	fixture.event(t, onceEvent(update.Policy.Rules[0]))
	fixture.reply(t, request)
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("failed manifest save returned policy success")
		}
	case <-time.After(time.Second):
		t.Fatal("failed save joined the egress worker while retaining its policy turn")
	}
	waitEventSignal(t, fixture.c.done, "failed publication control close")
	waitEventSignal(t, fixture.c.events.finished, "failed publication policy worker")
	waitEventSignal(t, fixture.c.events.controlClosed, "failed publication disarm worker")
	fixture.s.mu.Lock()
	armed := fixture.s.m.Armed
	fixture.s.mu.Unlock()
	fixture.s.portMu.Lock()
	closed := fixture.s.portsClosed
	fixture.s.portMu.Unlock()
	if armed || !closed {
		t.Fatalf("unrecorded policy kept a door open: armed=%v ports_closed=%v", armed, closed)
	}
}
