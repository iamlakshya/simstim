package client

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

func registrationTransportPriorFixture(t *testing.T, was *Host, now Host) (context.Context, func()) {
	t.Helper()
	staged, abort, err := StagePinOwned(t.Context(), now.Name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(abort)
	o, err := ObserveHostRegistrationContext(staged, HostChange{Kind: "registration prior fixture", Was: was, Now: &now})
	if err != nil {
		t.Fatal(err)
	}
	owned, release, err := LockHost(WithHostStateObservation(staged, o), now.Name, "registration prior fixture")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	return owned, release
}

func TestHostTransportRegistrationPriorIDUsesOriginalWasWithoutWrites(t *testing.T) {
	stateFDTestHome(t)
	old := Host{Name: "box", SSH: "old.example", Labels: map[string]string{"route": "old"}}
	prospective := cloneGrantHost(old)
	prospective.SSH = "new.example"
	prospective.Labels["route"] = "new"
	if err := SaveHosts(t.Context(), []Host{old}); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 32)
	if err := writeTiersFile(NewTiers(map[string]TierEntry{old.Name: {Tier: TierOwn, HostID: id}})); err != nil {
		t.Fatal(err)
	}
	owned, _ := registrationTransportPriorFixture(t, &old, prospective)
	beforeHosts, err := os.ReadFile(hostsPath())
	if err != nil {
		t.Fatal(err)
	}
	beforeTiers, err := os.ReadFile(tiersPath())
	if err != nil {
		t.Fatal(err)
	}
	startup, cleanup, attempt, err := beginHostTransportObservation(owned, prospective)
	if err != nil {
		t.Fatal("prospective registration was treated as ordinary saved Host:", err)
	}
	defer cleanup()
	if attempt.kind != hostTransportRegistration || attempt.priorID != id || attempt.observation.original == nil || !SameEntry(*attempt.observation.original, old) {
		t.Fatal("registration lost immutable prior Was identity")
	}
	c := generatedHostTransportConn(prospective, strings.Repeat("b", 32))
	// Main derives this from the actual accepted Hello comparison, without
	// writing the old Was tier. This fixture checks the frozen engine fact.
	c.hostIDChanged = true
	if err := finishHostTransportObservation(startup, attempt, c); err != nil {
		t.Fatal(err)
	}
	if c.hostTransport.priorID != id || !c.HostIDChanged() || !c.hostTransport.idChanged {
		t.Fatal("new HID lost its prior identity/change fact")
	}
	for _, pair := range []struct {
		path   string
		before []byte
	}{{hostsPath(), beforeHosts}, {tiersPath(), beforeTiers}} {
		got, err := os.ReadFile(pair.path)
		if err != nil || !bytes.Equal(got, pair.before) {
			t.Fatalf("prior capture stamped pending registration before final CAS: %s, %v", pair.path, err)
		}
	}
}

func TestHostTransportRegistrationMissingPriorFileDoesNotMigrate(t *testing.T) {
	for _, existingWas := range []bool{false, true} {
		t.Run(map[bool]string{false: "new-Was-nil", true: "changed-existing-Was"}[existingWas], func(t *testing.T) {
			stateFDTestHome(t)
			old := Host{Name: "box", SSH: "old.example"}
			prospective := Host{Name: "box", SSH: "new.example"}
			var was *Host
			if existingWas {
				if err := SaveHosts(t.Context(), []Host{old}); err != nil {
					t.Fatal(err)
				}
				was = &old
			}
			owned, _ := registrationTransportPriorFixture(t, was, prospective)
			_, cleanup, attempt, err := beginHostTransportObservation(owned, prospective)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if attempt.priorID != "" || attempt.kind != hostTransportRegistration {
				t.Fatal("missing prior identity fabricated saved/checked state")
			}
			terminalAssertAbsent(t, tiersPath(), tiersPath()+".lock")
			if !existingWas {
				terminalAssertAbsent(t, hostsPath())
			}
		})
	}
}

func TestHostTransportNewRegistrationDoesNotBorrowOrphanTierIdentity(t *testing.T) {
	stateFDTestHome(t)
	h := Host{Name: "new", SSH: "new.example"}
	if err := writeTiersFile(NewTiers(map[string]TierEntry{h.Name: {Tier: TierOwn, HostID: strings.Repeat("a", 32)}})); err != nil {
		t.Fatal(err)
	}
	owned, _ := registrationTransportPriorFixture(t, nil, h)
	startup, cleanup, attempt, err := beginHostTransportObservation(owned, h)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if attempt.priorID != "" {
		t.Fatal("Was=nil registration inherited an orphan tier UUID")
	}
	c := generatedHostTransportConn(h, "")
	if err := finishHostTransportObservation(startup, attempt, c); err != nil {
		t.Fatal(err)
	}
	if c.hostTransport.hostID != "" || attempt.observation.hasCheckedIdentity() {
		t.Fatal("empty registration hello became checked prior identity")
	}
}
