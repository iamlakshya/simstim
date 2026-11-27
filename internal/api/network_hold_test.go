package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPendingNetworkRequestIsDisplayDataWithFiniteCanonicalFields(t *testing.T) {
	opened := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	request := PendingNetworkRequest{Version: NetworkHoldV1, ID: strings.Repeat("a", 32), Revision: 1, Host: "authored.example", Port: 443, Opened: opened, Expires: opened.Add(5 * time.Minute), Waiting: 1, Status: NetworkHoldOpen}
	if !ValidPendingNetworkRequest(request) {
		t.Fatal("valid public held-request display data was refused")
	}
	encoded, err := json.Marshal(NetworkHoldSnapshot{Version: NetworkHoldV1, Revision: 1, Requests: []PendingNetworkRequest{request}})
	if err != nil {
		t.Fatal(err)
	}
	var decoded NetworkHoldSnapshot
	if err := json.Unmarshal(encoded, &decoded); err != nil || len(decoded.Requests) != 1 || !ValidPendingNetworkRequest(decoded.Requests[0]) {
		t.Fatal("finite public display envelope did not round trip", err)
	}
	for name, change := range map[string]func(*PendingNetworkRequest){
		"future-version":   func(r *PendingNetworkRequest) { r.Version++ },
		"no-revision":      func(r *PendingNetworkRequest) { r.Revision = 0 },
		"short-id":         func(r *PendingNetworkRequest) { r.ID = r.ID[1:] },
		"nonhex-id":        func(r *PendingNetworkRequest) { r.ID = strings.Repeat("x", 32) },
		"host-alias":       func(r *PendingNetworkRequest) { r.Host = "AUTHORED.example." },
		"host-control":     func(r *PendingNetworkRequest) { r.Host = "authored\x00.example" },
		"port":             func(r *PendingNetworkRequest) { r.Port = 65536 },
		"no-open-time":     func(r *PendingNetworkRequest) { r.Opened = time.Time{} },
		"backwards-time":   func(r *PendingNetworkRequest) { r.Expires = r.Opened },
		"time-overflow":    func(r *PendingNetworkRequest) { r.Expires = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) },
		"future-status":    func(r *PendingNetworkRequest) { r.Status = "approved" },
		"closed-waiters":   func(r *PendingNetworkRequest) { r.Status = NetworkHoldClosed },
		"too-many-waiters": func(r *PendingNetworkRequest) { r.Waiting = NetworkHoldWaiterLimit + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			bad := request
			change(&bad)
			if ValidPendingNetworkRequest(bad) {
				t.Fatal("unsupported public display data gained meaning")
			}
		})
	}
}

func TestNetworkHoldCompleteSnapshotKeepsMaskedIdentityAndAbsoluteExpiry(t *testing.T) {
	opened := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	first := PendingNetworkRequest{Version: NetworkHoldV1, ID: strings.Repeat("a", 32), Revision: 2, Host: "[EXPOSED].example.test", Redacted: true, Port: 443, Opened: opened, Expires: opened.Add(5 * time.Minute), Status: NetworkHoldOpen}
	second := first
	second.ID = strings.Repeat("b", 32)
	snapshot := NetworkHoldSnapshot{Version: NetworkHoldV1, Revision: 3, Requests: []PendingNetworkRequest{first, second}}
	if !ValidNetworkHoldSnapshot(snapshot) || snapshot.Requests[0].Waiting != 0 || !snapshot.Requests[0].Expires.Equal(opened.Add(5*time.Minute)) {
		t.Fatal("two masked targets or zero active waiters lost a complete retained snapshot")
	}
	if !ValidNetworkHoldSnapshot(NetworkHoldSnapshot{Version: NetworkHoldV1, Revision: 1, Stopped: true}) {
		t.Fatal("an empty stopped display snapshot lost its nonzero revision")
	}
	for _, change := range []func(*NetworkHoldSnapshot){
		func(s *NetworkHoldSnapshot) { s.Version++ },
		func(s *NetworkHoldSnapshot) { s.Revision = 0 },
		func(s *NetworkHoldSnapshot) { s.Requests[0].Revision = 4 },
		func(s *NetworkHoldSnapshot) { s.Requests[0].Status = NetworkHoldClosed },
		func(s *NetworkHoldSnapshot) { s.Requests[1].ID = s.Requests[0].ID },
	} {
		bad := snapshot
		bad.Requests = append([]PendingNetworkRequest(nil), snapshot.Requests...)
		change(&bad)
		if ValidNetworkHoldSnapshot(bad) {
			t.Fatal("unsupported complete snapshot acquired display meaning")
		}
	}
	tooMany := NetworkHoldSnapshot{Version: NetworkHoldV1, Revision: 3}
	for i := range NetworkHoldCapacity + 1 {
		row := first
		row.ID = fmt.Sprintf("%032x", i)
		tooMany.Requests = append(tooMany.Requests, row)
	}
	if ValidNetworkHoldSnapshot(tooMany) {
		t.Fatal("display envelope exceeded the existing pending capacity")
	}
}
