package host

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestRestoreCoordinatorSchemaIsClosedAndPurgeReadable(t *testing.T) {
	raw, err := json.Marshal(restoreGateDataV1{Version: 1, HostID: strings.Repeat("a", 32), UID: os.Getuid()})
	if err != nil || validateStateMetadata(stateRestoreGate, raw) != nil {
		t.Fatal("the fixed coordinator role is not readable by the existing metadata/purge validator", err)
	}
	for _, invalid := range []string{`null`, `{}`, `{"version":1,"host_id":"` + strings.Repeat("a", 32) + `","uid":null}`, `{"version":2,"host_id":"` + strings.Repeat("a", 32) + `","uid":0}`, string(raw) + ` {}`, strings.Replace(string(raw), `"version":1`, `"version":1,"Version":1`, 1)} {
		if err := validateStateMetadata(stateRestoreGate, []byte(invalid)); err == nil {
			t.Fatal("a future, null, ambiguous or trailing coordinator record was accepted", invalid)
		}
	}
	for _, gate := range []*restoreGateV1{nil, {}, {publisher: &stateMetadataPublisher{}}} {
		if err := gate.Check(); err == nil {
			t.Fatal("coordinator data supplied a native publisher or ownership lifetime")
		}
	}
}

// These are lower native file-lock oracles. They issue no publisher, helper,
// session operation, execution, stopped source or capacity permission.
func restoreLockFixtureV1(t *testing.T) (*os.Root, *restoreGateV1) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := cachefs.OpenPrivateRoot(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	file, err := cachefs.OpenFile(state, string(stateRestoreGate), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	if _, err := file.Write([]byte("original coordinator")); err != nil {
		t.Fatal(err)
	}
	reference, err := cachefs.CaptureFile(state, string(stateRestoreGate), file, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	return state, &restoreGateV1{state: state, file: file, reference: reference}
}

func TestRestoreCoordinatorNativeLockExcludesAnotherDescriptor(t *testing.T) {
	state, first := restoreLockFixtureV1(t)
	if err := first.lockOriginalV1(); err != nil {
		t.Fatal(err)
	}
	secondFile, err := cachefs.OpenFile(state, string(stateRestoreGate), os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer secondFile.Close()
	second := &restoreGateV1{state: state, file: secondFile, reference: first.reference}
	var failure *proto.Error
	if err := second.lockOriginalV1(); !errors.As(err, &failure) || failure.Code != "busy" || failure.Hint == "" {
		t.Fatal("the same host coordinator admitted two concurrent restore flows", err)
	}
	if err := first.file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.lockOriginalV1(); err != nil {
		t.Fatal("the original coordinator did not admit a new flow after its owner closed", err)
	}
}

func TestRestoreCoordinatorCannotFollowReplacementWhileOriginalIsLocked(t *testing.T) {
	state, first := restoreLockFixtureV1(t)
	if err := first.lockOriginalV1(); err != nil {
		t.Fatal(err)
	}
	if err := state.Rename(string(stateRestoreGate), "retained-original"); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteFile(string(stateRestoreGate), []byte("replacement coordinator"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := first.lockOriginalV1(); err == nil {
		t.Fatal("the original locked descriptor followed a replacement coordinator")
	}
	if data, err := state.ReadFile(string(stateRestoreGate)); err != nil || string(data) != "replacement coordinator" {
		t.Fatal("rejected replacement was changed or removed", err)
	}
}
