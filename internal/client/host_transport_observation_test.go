package client

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func hostTransportSavedFixture(t *testing.T) Host {
	t.Helper()
	stateFDTestHome(t)
	h := Host{Name: "box", SSH: "transport.example"}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	return h
}

// This exercises constructor/source semantics with generated objects only.
// Main separately tests the actual Dial/Hello/H4 sole production call site.
func finishGeneratedTransportFixture(t *testing.T, h Host, hostID string, features ...string) *Conn {
	t.Helper()
	owned, cleanup, attempt, err := beginHostTransportObservation(t.Context(), h)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	c := generatedHostTransportConn(h, hostID, features...)
	if err := finishHostTransportObservation(owned, attempt, c); err != nil {
		t.Fatal(err)
	}
	return c
}

func transportPublicationFixture(t *testing.T, h Host, hostID string) (context.Context, *HostStateObservation, func()) {
	t.Helper()
	owned, release, err := LockHost(t.Context(), h.Name, "publication fixture")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	o, err := ObserveHostStateContext(owned, h, hostID)
	if err != nil {
		t.Fatal(err)
	}
	return WithHostStateObservation(owned, o), o, release
}

func TestHostTransportManualConnCannotBindRawExpectedID(t *testing.T) {
	for _, hostID := range []string{"", strings.Repeat("a", 32)} {
		t.Run(hostID, func(t *testing.T) {
			h := hostTransportSavedFixture(t)
			ctx, observation, _ := transportPublicationFixture(t, h, hostID)
			manual := generatedHostTransportConn(h, hostID)
			if err := manual.BindHostStateObservation(ctx); err == nil || observation.hasCheckedIdentity() || observation.hasLegacyBinding() {
				t.Fatal("private Proc/raw expected ID without an actual Dial stamp became authority")
			}
			terminalAssertAbsent(t, filepath.Join(StateDir(), "hosts", HostFileKey(h.Name)))
		})
	}
}

func TestHostTransportSourceSurvivesOwnDialOPButRejectsIdenticalReaddABA(t *testing.T) {
	h := hostTransportSavedFixture(t)
	id := strings.Repeat("a", 32)
	c := finishGeneratedTransportFixture(t, h, id, "session.id")
	// The short Dial OP is gone; a current publication has its OWN actual OP.
	ctx, _, release := transportPublicationFixture(t, h, id)
	if err := c.BindHostStateObservation(ctx); err != nil {
		t.Fatal("same original Source could not use the next actual OP:", err)
	}
	// End this publication before the real canonical bulk ABA schedule.
	release()
	if err := SaveHosts(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	next, observation, _ := transportPublicationFixture(t, h, id)
	if err := c.BindHostStateObservation(next); err == nil || observation.hasCheckedIdentity() {
		t.Fatal("old Conn adopted remove-and-identical-readd Source despite same HID and native OP")
	}
	terminalAssertAbsent(t, filepath.Join(StateDir(), grantsFileName), removalDir(h.Name))
}

func TestHostTransportFinishedStampRefusesChangedConnFacts(t *testing.T) {
	for _, change := range []string{"host", "host-id", "proc", "transport", "features", "id-changed", "agent-route", "closed"} {
		t.Run(change, func(t *testing.T) {
			h := hostTransportSavedFixture(t)
			id := strings.Repeat("a", 32)
			c := finishGeneratedTransportFixture(t, h, id, "session.id", "resume")
			switch change {
			case "host":
				c.Host.SSH = "replacement.example"
			case "host-id":
				c.hostID = strings.Repeat("b", 32)
			case "proc":
				c.proc = &hostStateGeneratedProc{}
			case "transport":
				c.route.t = &hostStateGeneratedTransport{}
			case "features":
				c.Features = c.Features[:1]
			case "id-changed":
				c.hostIDChanged = true
			case "agent-route":
				c.agentOnly = true
			case "closed":
				close(c.done)
			}
			ctx, o, _ := transportPublicationFixture(t, h, id)
			if err := c.BindHostStateObservation(ctx); err == nil || o.hasCheckedIdentity() || o.hasLegacyBinding() {
				t.Fatal("changed Conn facts rebound the original transport Source")
			}
		})
	}
}

func TestHostTransportEmptyIDIsDistinctAndKnownPriorRefusesAuthorityOnly(t *testing.T) {
	for _, knownPrior := range []bool{false, true} {
		t.Run(map[bool]string{false: "genuine-empty", true: "known-before-H4"}[knownPrior], func(t *testing.T) {
			h := hostTransportSavedFixture(t)
			hosts, err := LoadHosts()
			if err != nil {
				t.Fatal(err)
			}
			tiers, err := LoadTiersContext(t.Context(), hosts)
			if err != nil {
				t.Fatal(err)
			}
			entry, _ := tiers.Entry(h.Name)
			if knownPrior {
				entry.HostID = strings.Repeat("a", 32)
				tiers.entries[h.Name] = entry
				if err := writeTiersFile(tiers); err != nil {
					t.Fatal(err)
				}
			}
			owned, cleanup, attempt, err := beginHostTransportObservation(t.Context(), h)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			c := generatedHostTransportConn(h, "", "session.id")
			if knownPrior {
				// Simulate H4's accepted downgrade, which resets the stored ID.
				entry.HostID, entry.IDChanged, entry.Tier = "", true, TierShared
				tiers.entries[h.Name] = entry
				if err := writeTiersFile(tiers); err != nil {
					t.Fatal(err)
				}
				c.hostIDChanged = true
			}
			if err := finishHostTransportObservation(owned, attempt, c); err != nil {
				t.Fatal("accepted read-only connection was blocked by a value-authority refusal:", err)
			}
			cleanup()
			ctx, o, _ := transportPublicationFixture(t, h, "")
			err = c.BindHostStateObservation(ctx)
			if knownPrior {
				if err == nil || o.hasLegacyBinding() {
					t.Fatal("post-H4 empty record erased the known pre-Dial ID")
				}
			} else if err != nil || !o.hasLegacyBinding() || o.hasCheckedIdentity() || o.hostID != "" {
				t.Fatalf("genuine empty-ID class was lost or fabricated checked identity: %v", err)
			}
		})
	}
}

func TestHostTransportSourceChangeDuringAttemptAndLateFinishRefuse(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(map[bool]string{false: "changed-during-Hello", true: "after-cleanup"}[late], func(t *testing.T) {
			h := hostTransportSavedFixture(t)
			owned, cleanup, attempt, err := beginHostTransportObservation(t.Context(), h)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if late {
				cleanup()
			} else if err := SaveHosts(t.Context(), []Host{h, {Name: "other", SSH: "other.example"}}); err != nil {
				t.Fatal(err)
			}
			c := generatedHostTransportConn(h, strings.Repeat("a", 32))
			if err := finishHostTransportObservation(owned, attempt, c); err == nil || c.hostTransport != nil {
				t.Fatal("ended or changed Source attempt minted a fresh transport stamp")
			}
		})
	}
}

func TestHostTransportUnsavedProvisionalCreatesNoOPAndCannotPromote(t *testing.T) {
	stateFDTestHome(t)
	h := Host{Name: "new", SSH: "new.example"}
	c := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32))
	if c.hostTransport.kind != hostTransportProvisional {
		t.Fatal("unobserved unsaved Dial fabricated saved membership")
	}
	terminalAssertAbsent(t, StateDir(), hostLockPath(h.Name), removalDir(h.Name))
	if err := SaveHostContext(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	ctx, o, _ := transportPublicationFixture(t, h, c.HostID())
	if err := c.BindHostStateObservation(ctx); err == nil || o.hasCheckedIdentity() {
		t.Fatal("pre-save provisional Conn promoted after publication")
	}
	if _, err := os.Lstat(removalDir(h.Name)); !os.IsNotExist(err) {
		t.Fatalf("refused promotion created host JSON: %v", err)
	}
}

func TestHostTransportRegistrationKeepsOriginalStageOPSourceAndCannotPromote(t *testing.T) {
	stateFDTestHome(t)
	h := Host{Name: "registration", SSH: "registration.example"}
	staged, abort, err := StagePinOwned(t.Context(), h.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer abort()
	o, err := ObserveHostRegistrationContext(staged, HostChange{Kind: "host add", Now: &h})
	if err != nil {
		t.Fatal(err)
	}
	bound := WithHostStateObservation(staged, o)
	if _, cleanup, _, err := beginHostTransportObservation(bound, h); err == nil {
		cleanup()
		t.Fatal("registration transport started before the actual original OP")
	}
	owned, release, err := LockHost(bound, h.Name, "original registration")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	transportCtx, cleanup, attempt, err := beginHostTransportObservation(owned, h)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	c := generatedHostTransportConn(h, strings.Repeat("a", 32))
	if err := finishHostTransportObservation(transportCtx, attempt, c); err != nil {
		t.Fatal(err)
	}
	if c.hostTransport.registration != o || c.hostTransport.kind != hostTransportRegistration {
		t.Fatal("registration transport refreshed its original observation")
	}
	if err := c.BindHostStateObservation(owned); err != nil {
		t.Fatal(err)
	}
	cleanup()
	// A deliberate low-level writer fixture rotates first publication while
	// the original physical OP remains. Production H15 tests use its actual CAS.
	if err := writeHostsFile([]Host{h}); err != nil {
		t.Fatal(err)
	}
	next, err := ObserveHostStateContext(owned, h, c.HostID())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.BindHostStateObservation(WithHostStateObservation(owned, next)); err == nil || next.hasCheckedIdentity() {
		t.Fatal("pre-save registration Conn promoted into saved publication")
	}
	abort()
	if err := c.BindHostStateObservation(owned); err == nil {
		t.Fatal("closed registration stage renewed old transport authority")
	}
}

func TestHostTransportOriginalOwnerCloseCancelsStartupBeforePhysicalClose(t *testing.T) {
	h, owner, _, release := hostObservationFixture(t)
	startup, cleanup, _, err := beginHostTransportObservation(owner, h)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	closed := make(chan struct{})
	go func() { release(); close(closed) }()
	hostStateLifetimeAwait(t, startup.Done())
	hostStateLifetimeStaysBlocked(t, closed)
	cleanup()
	hostStateLifetimeAwait(t, closed)
}

func TestHostTransportNestedDialReusesOnlySameUnfinishedAttempt(t *testing.T) {
	h := hostTransportSavedFixture(t)
	startup, cleanup, original, err := beginHostTransportObservation(t.Context(), h)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	nested, leaveNested, same, err := beginHostTransportObservation(startup, h)
	if err != nil || same != original {
		t.Fatalf("nested actual dial reminted its Source/attempt: %v", err)
	}
	leaveNested()
	if err := checkHostTransportAttempt(nested, original, h); err != nil {
		t.Fatal("nested cleanup ended the outer Dial attempt:", err)
	}
	c := generatedHostTransportConn(h, strings.Repeat("a", 32))
	if err := finishHostTransportObservation(nested, original, c); err != nil {
		t.Fatal(err)
	}
	if _, release, _, err := beginHostTransportObservation(startup, h); err == nil {
		release()
		t.Fatal("successful finished Dial attempt minted another Conn")
	}
}

func TestHostTransportProvisionalGuardRequiresContinuedAbsenceWithoutPromotion(t *testing.T) {
	stateFDTestHome(t)
	h := Host{Name: "provisional", SSH: "provisional.example"}
	startup, cleanup, attempt, err := beginHostTransportObservation(t.Context(), h)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := checkHostTransportAttempt(startup, attempt, h); err != nil {
		t.Fatal(err)
	}
	terminalAssertAbsent(t, hostLockPath(h.Name), removalDir(h.Name))
	// Another actual metadata operation publishes the same typed values.
	if err := SaveHostContext(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	if err := checkHostTransportAttempt(startup, attempt, h); err == nil {
		t.Fatal("old provisional attempt adopted new saved membership")
	}
	if attempt.kind != hostTransportProvisional || attempt.observation != nil || attempt.op != nil {
		t.Fatal("continued-absence refusal fabricated saved Source/OP authority")
	}
	c := generatedHostTransportConn(h, strings.Repeat("a", 32))
	if err := finishHostTransportObservation(startup, attempt, c); err == nil || c.hostTransport != nil {
		t.Fatal("provisional accepted hello promoted after the name became saved")
	}
}
