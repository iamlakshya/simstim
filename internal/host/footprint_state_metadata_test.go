package host

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/proto"
)

func footprintStateTestDataV1(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(footprintLedger{Version: 1, HostID: strings.Repeat("a", 32), UID: os.Getuid(), Entries: []Footprint{}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestFootprintStateRoleIsExactAndBounded(t *testing.T) {
	raw := footprintStateTestDataV1(t)
	if stateFootprint != stateMetadataRole("footprint.json") || !validStateMetadataRole(stateFootprint) || !validStateMetadataRole(stateLockdown) {
		t.Fatal("the exact footprint registration displaced another typed role")
	}
	if err := validateStateMetadata(stateFootprint, raw); err != nil {
		t.Fatal(err)
	}
	for _, role := range []stateMetadataRole{"arbitrary.json", "../footprint.json", "footprint.json.bak"} {
		if err := validateStateMetadata(role, raw); err == nil {
			t.Fatal("footprint validation introduced a broad metadata role exemption", role)
		}
	}
	for _, malformed := range [][]byte{
		[]byte(`{"boot_id":"boot","busy_at":"2026-01-01T00:00:00Z"}`),
		append(append([]byte(nil), raw...), []byte(` {}`)...),
		bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":2`), 1),
		bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"future_owner":true`), 1),
		bytes.Repeat([]byte(" "), proto.MaxFrame+1),
	} {
		if err := validateStateMetadata(stateFootprint, malformed); err == nil {
			t.Fatal("the footprint role accepted idle, future, trailing or oversized data")
		}
	}
}

func TestFootprintStateReaderValidatesOriginalDescriptor(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := cachefs.OpenPrivateRoot(directory, false)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	raw := footprintStateTestDataV1(t)
	path := filepath.Join(directory, string(stateFootprint))
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	read, info, err := readStateMetadata(context.Background(), state, stateFootprint)
	if err != nil || info == nil || !bytes.Equal(read, raw) {
		t.Fatal("the typed footprint reader did not validate the actual file descriptor", err)
	}
	invalid := bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":2`), 1)
	if err := os.WriteFile(path, invalid, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readStateMetadata(context.Background(), state, stateFootprint); err == nil {
		t.Fatal("the footprint reader accepted an unknown archive version")
	}
	if current, err := os.ReadFile(path); err != nil || !bytes.Equal(current, invalid) {
		t.Fatal("a rejected footprint archive was changed by its reader", err)
	}
}

func TestFootprintStateReplayPreservesInvalidPreparedArchive(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := cachefs.OpenPrivateRoot(directory, false)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	leaf := ".metadata-part-" + strings.Repeat("b", 32)
	invalid := bytes.Replace(footprintStateTestDataV1(t), []byte(`"version":1`), []byte(`"version":2`), 1)
	if err := os.WriteFile(filepath.Join(directory, leaf), invalid, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := state.Lstat(leaf)
	if err != nil {
		t.Fatal(err)
	}
	// This negative uses the genuine prepared inode. It constructs no runtime
	// source or ownership claim from the public footprint archive.
	intent := metadataWriteIntent{After: refOf(info), Temporary: leaf}
	control := &placementControl{StateWrites: map[string]metadataWriteIntent{string(stateFootprint): intent}}
	if err := cancelStatePrepared(context.Background(), nil, control, state, stateFootprint, intent); err == nil {
		t.Fatal("state replay removed a prepared footprint before validating its schema")
	}
	if current, err := state.Lstat(leaf); err != nil || !intent.After.matches(current) {
		t.Fatal("invalid prepared footprint lost its original inode", err)
	}
	if current, err := os.ReadFile(filepath.Join(directory, leaf)); err != nil || !bytes.Equal(current, invalid) {
		t.Fatal("state replay changed a rejected prepared footprint", err)
	}
}
