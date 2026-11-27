package egress

import "testing"

func TestHTTPMethodShutdownRejectsLateSplicedResourceAdmission(t *testing.T) {
	tracker := newFlowTracker(maxActiveFlows)
	tracker.closeAll()
	cancelled := false
	if id := tracker.add(activeFlow{cancel: func() { cancelled = true }}); id != 0 || !cancelled {
		t.Fatalf("a late external flow escaped shutdown: id=%d cancelled=%v", id, cancelled)
	}
	if len(tracker.snapshot()) != 0 {
		t.Fatal("shutdown retained a late external flow")
	}
}
