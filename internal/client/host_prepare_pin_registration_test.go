package client

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestHostPrepareCommittedPinChangeRefusesBeforeTransport(t *testing.T) {
	stateFDTestHome(t)
	h := Host{Name: "box", SSH: "box.example"}
	key, replacement := prepareTestHostKey(1), prepareTestHostKey(2)
	staged, abort, err := StagePinOwned(t.Context(), h.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer abort()
	stage, err := stageForContext(staged, h.Name)
	if err != nil || stage == nil {
		t.Fatal("the original native pin stage is missing", err)
	}
	if err := writePin(h.Name, stage, []hostKey{key}); err != nil {
		t.Fatal(err)
	}
	change := HostChange{Kind: "the launcher", Now: &h, Pin: true, Tier: TierOwn, HostID: strings.Repeat("a", 32)}
	observation, err := ObserveHostRegistrationContext(staged, change)
	if err != nil {
		t.Fatal(err)
	}
	registered := WithHostStateObservation(staged, observation)
	target, err := UpdateHostsForPreparation(registered, change)
	abort()
	if err != nil {
		t.Fatal(err)
	}
	if target.key != key.Fingerprint() {
		t.Fatal("the actual registration writer lost its selected fingerprint")
	}
	// Replace only the actual pin file after commit. The original saved
	// membership, tier and target stay unchanged and must refuse this key.
	if err := writePin(h.Name, nil, []hostKey{replacement}); err != nil {
		t.Fatal(err)
	}
	transport := &prepareTestTransport{serve: func(context.Context) (Proc, error) {
		return nil, errors.New("unexpected transport start after native pin replacement")
	}}
	conn, err := target.dialRoute(t.Context(), route{t: transport})
	if conn != nil {
		conn.Close()
		t.Fatal("a changed native pin opened a saved preparation connection")
	}
	var failure *UserError
	if !errors.As(err, &failure) || failure.Kind != KindRefused || failure.Code != "host_key_unreadable" {
		t.Fatalf("unexpected pin replacement refusal: %v", err)
	}
	if transport.starts.Load() != 0 {
		t.Fatal("the unexpected key reached a transport before refusal")
	}
	saved, err := FindHost(h.Name)
	if err != nil || !SameEntry(saved, h) || saved.WallFloor != "" || saved.Prepared != nil {
		t.Fatalf("refused pin changed the saved host or prepared floor: %+v %v", saved, err)
	}
}
