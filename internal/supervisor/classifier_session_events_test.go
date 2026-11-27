package supervisor

import (
	"fmt"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

func TestClassifierSessionQueueCoalescesOnlyClosedOpaqueActionsWithinHoldCapacity(t *testing.T) {
	queue := newEgressEvents()
	for index := range api.NetworkHoldCapacity {
		args := api.NetworkHoldActionArgs{Version: 1, Session: "primary2", Request: fmt.Sprintf("%032x", index+1), Revision: 1, Scope: api.NetworkHoldSession, Maximum: api.NetworkHoldPublicOnly}
		if !queue.enqueue(egress.CtlEvent{Event: egress.EventClassifierSession, ClassifierSession: &args}, 128) {
			t.Fatal("valid optional opaque action refused")
		}
		args.Revision = 3
		if !queue.enqueue(egress.CtlEvent{Event: egress.EventClassifierSession, ClassifierSession: &args}, 128) {
			t.Fatal("same hold did not coalesce")
		}
	}
	extra := api.NetworkHoldActionArgs{Version: 1, Session: "primary2", Request: fmt.Sprintf("%032x", api.NetworkHoldCapacity+1), Revision: 1, Scope: api.NetworkHoldSession, Maximum: api.NetworkHoldPublicOnly}
	if !queue.enqueue(egress.CtlEvent{Event: egress.EventClassifierSession, ClassifierSession: &extra}, 128) {
		t.Fatal("optional proposal pressure stopped unrelated control")
	}
	batch := queue.takeClassifierSessions()
	if len(batch) != api.NetworkHoldCapacity {
		t.Fatal("optional action queue exceeded original hold capacity")
	}
	for _, args := range batch {
		if args.Revision != 3 || args.Scope != api.NetworkHoldSession || args.Maximum != api.NetworkHoldPublicOnly {
			t.Fatal("coalescing changed action restrictions")
		}
	}
	extra.Scope = api.NetworkHoldProfile
	if queue.enqueue(egress.CtlEvent{Event: egress.EventClassifierSession, ClassifierSession: &extra}, 128) {
		t.Fatal("classifier event supplied wider policy authority")
	}
}

func TestJudgeUnavailableEventIsOncePerOwnedControlAndCarriesNoGuestDetail(t *testing.T) {
	queue := newEgressEvents()
	event := egress.CtlEvent{Event: egress.EventJudgeUnavailable}
	if !queue.enqueue(event, 32) || !queue.enqueue(event, 32) || !queue.takeJudgeUnavailable() || queue.takeJudgeUnavailable() {
		t.Fatal("outage event was dropped or duplicated in queue")
	}
	if !queue.enqueue(event, 32) || queue.takeJudgeUnavailable() {
		t.Fatal("outage event repeated after accepted worker delivery")
	}
}
