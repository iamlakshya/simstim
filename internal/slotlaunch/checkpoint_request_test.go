//go:build linux

package slotlaunch

import (
	"encoding/json"
	"testing"
)

func TestCheckpointRequestsHaveOnlyFixedRolesAndBoundedMetadata(t *testing.T) {
	for _, action := range []string{"open", "probe", "list", "trim", "create", "select"} {
		t.Run(action, func(t *testing.T) {
			request := checkpointRequestV1{Op: "checkpoint", Session: "abc234de", Action: action}
			if action == "trim" {
				request.KeepCount = 10
			}
			if action == "create" || action == "select" {
				request.Name = "20261121T120000.000Z"
			}
			if action == "create" {
				request.MetaSize = 128
			}
			raw, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := Parse(raw)
			if err != nil || parsed.Action != action || parsed.Session != request.Session || parsed.Name != request.Name {
				t.Fatalf("fixed checkpoint request refused: %+v %v", parsed, err)
			}
		})
	}
	for _, change := range []func(*checkpointRequestV1){
		func(r *checkpointRequestV1) { r.Action = "restore" },
		func(r *checkpointRequestV1) { r.Session = "../outside" },
		func(r *checkpointRequestV1) { r.Name = "../../outside" },
		func(r *checkpointRequestV1) { r.Name = "20261121T120000.000Z.disk" },
		func(r *checkpointRequestV1) { r.KeepCount = -1 },
		func(r *checkpointRequestV1) { r.KeepCount = 1025 },
		func(r *checkpointRequestV1) { r.MetaSize = (64 << 10) + 1 },
		func(r *checkpointRequestV1) { r.Generation = "copied-journal-authority" },
	} {
		request := checkpointRequestV1{Op: "checkpoint", Session: "abc234de", Action: "create", Name: "20261121T120000.000Z", MetaSize: 128}
		change(&request)
		raw, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(raw); err == nil {
			t.Fatalf("unbounded or non-role checkpoint request accepted: %s", raw)
		}
	}
}

func TestCheckpointWireCannotSupplyRootPathOrNativeAuthority(t *testing.T) {
	for _, raw := range []string{
		`{"op":"checkpoint","session":"abc234de","action":"probe","name":"","generation":"","keep_count":0,"meta_size":0,"path":"/etc/shadow"}`,
		`{"op":"checkpoint","session":"abc234de","action":"probe","name":"","generation":"","keep_count":0,"meta_size":0,"fd":9}`,
		`{"op":"checkpoint","session":"abc234de","action":"probe","name":"","generation":"","keep_count":0,"meta_size":0,"stopped":true}`,
		`{"op":"checkpoint","session":"abc234de","action":"probe","name":"","generation":"","keep_count":0,"meta_size":0,"session":"abc234df"}`,
	} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Fatalf("checkpoint data minted an unrelated role: %s", raw)
		}
	}
}

func TestCheckpointReadAndSpaceRemainFixedObservations(t *testing.T) {
	for _, raw := range []string{
		`{"op":"checkpoint.read","session":"abc234de","action":"check","name":"20261121T120000.000Z","generation":"","keep_count":0,"meta_size":0}`,
		`{"op":"checkpoint.space","session":"abc234de","needed":1073741824}`,
	} {
		if _, err := Parse([]byte(raw)); err != nil {
			t.Fatal("valid fixed observation refused", err)
		}
	}
	for _, raw := range []string{
		`{"op":"checkpoint.read","session":"abc234de","action":"create","name":"20261121T120000.000Z","generation":"","keep_count":0,"meta_size":0}`,
		`{"op":"checkpoint.read","session":"abc234de","action":"check","name":"../../disk.img","generation":"","keep_count":0,"meta_size":0}`,
		`{"op":"checkpoint.space","session":"abc234de","needed":0}`,
		`{"op":"checkpoint.space","session":"abc234de","needed":1073741824,"stopped":true}`,
	} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Fatal("an observation accepted mutation or unrelated authority fields", raw)
		}
	}
}
