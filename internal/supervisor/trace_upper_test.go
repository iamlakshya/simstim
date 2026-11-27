package supervisor

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/proto"
)

// These metadata unit tests exercise no native runtime and grant no original
// startup, file, process or execution authority. Real assurance is E2E-only.
func TestTraceUpperRetainsAllControlledKeysWithSplitOutput(t *testing.T) {
	s := &Supervisor{id: "abcd2345", generation: "diagnostic-test"}
	c := &client{}
	s.traceUpperAttachV1(c, proto.Attach{Trace: "capture.1", Categories: []string{diag.CatAttach}})
	for range 1000 {
		s.traceUpperBridgeV1(c, proto.EncodeTime(diag.Now()))
		s.traceUpperInputV1(c, 13)
		for range 8 {
			s.traceUpperOutputV1(c)
		}
	}
	snapshot := s.traceUpperSnapshotV1()
	if snapshot.Dropped || len(snapshot.Samples) != 1000 {
		t.Fatalf("split ACK frames lost required samples: %d, dropped=%v", len(snapshot.Samples), snapshot.Dropped)
	}
	for index, row := range snapshot.Samples {
		if row.Key != int64(index+1) || row.Frames != 8 || row.End < row.Received || row.Received < row.Bridge {
			t.Fatalf("incomplete native-clock window: %+v", row)
		}
	}
	snapshot.Samples[0].End = 0
	if s.traceUpperSnapshotV1().Samples[0].End == 0 {
		t.Fatal("a returned diagnostic DTO mutated the actual observer")
	}
}

func TestTraceUpperPartialInputDoesNotConsumeOutputWindows(t *testing.T) {
	s := &Supervisor{}
	c := &client{}
	s.traceUpperAttachV1(c, proto.Attach{Trace: "capture.1"})
	for range 1000 {
		for range 5 {
			s.traceUpperBridgeV1(c, proto.EncodeTime(diag.Now()))
			s.traceUpperInputV1(c, 1)
		}
		s.traceUpperOutputV1(c)
	}
	got := s.traceUpperSnapshotV1()
	if got.Dropped || len(got.Samples) != 1000 || got.Samples[999].Key != 5000 {
		t.Fatal("partial input exhausted the controlled output-window ring")
	}
}

func TestTraceUpperRejectsMissingFramesAndBoundsItsOwnRing(t *testing.T) {
	s := &Supervisor{}
	c := &client{}
	s.traceUpperInputV1(c, 1)
	s.traceUpperOutputV1(c)
	if len(s.traceUpperSnapshotV1().Samples) != 0 {
		t.Fatal("untraced output acquired a key identity")
	}
	s.traceUpperAttachV1(c, proto.Attach{Trace: "capture.1"})
	s.traceUpperInputV1(c, 1)
	if !s.traceUpperSnapshotV1().Dropped {
		t.Fatal("a missing original bridge timestamp was silently accepted")
	}
	s = &Supervisor{}
	for range traceUpperLimitV1 + 1 {
		s.traceUpperBridgeV1(c, proto.EncodeTime(diag.Now()))
		s.traceUpperInputV1(c, 1)
		s.traceUpperOutputV1(c)
	}
	got := s.traceUpperSnapshotV1()
	if !got.Dropped || len(got.Samples) != traceUpperLimitV1 {
		t.Fatal("a partial capture looked complete or grew beyond its ring bound")
	}
}
