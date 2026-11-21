package host

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestExplicitStopProjectionKeepsPriorEndAndOOMDiagnostics(t *testing.T) {
	saved := autoRestoreFixture("")
	at := time.Unix(100, 0).UTC()
	saved.Life = &Life{Phase: LifeEnded, Started: at.Add(-time.Minute), End: &LifeEnd{Kind: EndSandbox, Reason: "the owned wait failed before later physical settlement", At: at, OOM: true, OOMAgent: true, Claimed: true}}
	code := 137
	saved.Status = Status{State: StateErrored, Since: at, ExitCode: &code, OOM: true, Reason: saved.Life.End.Reason}
	saved.Armed = true
	saved.VMCaptureNonce = strings.Repeat("a", 32)
	before := CloneManifest(*saved)
	next, err := explicitStopProjectionV1(saved, at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if next.Life.Phase != LifeStopped || next.Life.End.Kind != EndStop || next.Status.State != StateStopped || next.Armed || !next.Life.End.OOM || !next.Life.End.OOMAgent || !next.Life.End.Claimed || !strings.Contains(next.Status.Reason, saved.Life.End.Reason) {
		t.Fatal("explicit stop lost its actual state or previous diagnostics")
	}
	var diagnostic stopDiagnosticsV1
	if json.Unmarshal(next.StopDiagnosticsV1, &diagnostic) != nil || diagnostic.Version != 1 || diagnostic.Generation != saved.Run.Generation || !reflect.DeepEqual(diagnostic.Life, saved.Life) || !reflect.DeepEqual(diagnostic.Status, saved.Status) {
		t.Fatal("the original end/status record was not retained")
	}
	if next.VMCaptureNonce != saved.VMCaptureNonce || !reflect.DeepEqual(*saved, before) {
		t.Fatal("stop projection certified a capture or mutated its original source")
	}
}

func TestExplicitStopProjectionPreservesUnknownPriorDiagnostics(t *testing.T) {
	saved := autoRestoreFixture("")
	saved.StopDiagnosticsV1 = json.RawMessage(`{"version":2,"future_owner":"data-only"}`)
	before := bytes.Clone(saved.StopDiagnosticsV1)
	if _, err := explicitStopProjectionV1(saved, time.Now()); err == nil || !bytes.Equal(saved.StopDiagnosticsV1, before) {
		t.Fatal("stop publication overwrote unknown diagnostic data")
	}
	saved.Life = &Life{Phase: LifeStopped, End: &LifeEnd{Kind: EndStop}}
	saved.Status.State = StateStopped
	next, err := explicitStopProjectionV1(saved, time.Now())
	if err != nil || !bytes.Equal(next.StopDiagnosticsV1, before) {
		t.Fatal("an already recorded stop did not retain its opaque diagnostics", err)
	}
	cloned := CloneManifest(*saved)
	cloned.StopDiagnosticsV1[0] = 'x'
	if !bytes.Equal(saved.StopDiagnosticsV1, before) {
		t.Fatal("a manifest clone retained mutable aliases to diagnostic data")
	}
}
