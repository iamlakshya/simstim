package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostTransportParticipantPermitsMetadataButTerminalDrainsBoth(t *testing.T) {
	life := newHostStateLifetime()
	defer life.beginClose()
	transport, leaveTransport, err := life.enterTransport(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer leaveTransport()
	endMutation, err := life.mutate(t.Context())
	if err != nil {
		t.Fatal("actual H4 tier-only mutation self-joined its transport participant:", err)
	}
	endMutation()
	ordinary, leaveOrdinary, err := life.enterTransaction(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer leaveOrdinary()
	if end, err := life.mutate(t.Context()); err == nil {
		end()
		t.Fatal("ordinary state/publication participant silently upgraded into metadata mutation")
	}
	retired, finish := make(chan struct{}), make(chan struct{})
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		end, err := life.retire(t.Context())
		if err != nil {
			t.Error(err)
			return
		}
		defer end()
		close(retired)
		<-finish
	}()
	hostStateLifetimeAwait(t, ordinary.Done())
	hostStateLifetimeAwait(t, transport.Done())
	leaveOrdinary()
	hostStateLifetimeStaysBlocked(t, retired)
	leaveTransport()
	hostStateLifetimeAwait(t, retired)
	close(finish)
	hostStateLifetimeAwait(t, ended)
}

func TestHostTransportPhysicalCloseCancelsAndDrainsBothKinds(t *testing.T) {
	life := newHostStateLifetime()
	ordinary, leaveOrdinary, err := life.enterTransaction(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer leaveOrdinary()
	transport, leaveTransport, err := life.enterTransport(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer leaveTransport()
	life.beginClose()
	hostStateLifetimeAwait(t, ordinary.Done())
	hostStateLifetimeAwait(t, transport.Done())
	closed := make(chan struct{})
	go func() { life.drain(); close(closed) }()
	leaveOrdinary()
	hostStateLifetimeStaysBlocked(t, closed)
	leaveTransport()
	hostStateLifetimeAwait(t, closed)
	if _, release, err := life.enterTransport(t.Context()); err == nil {
		release()
		t.Fatal("a physically closed owner admitted new transport work")
	}
}

func TestHostTransportCancelledRetirementNeverRevivesOldWorkContexts(t *testing.T) {
	life := newHostStateLifetime()
	defer life.beginClose()
	old, releaseOld, err := life.enterTransport(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer releaseOld()
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() { _, err := life.retire(ctx); result <- err }()
	hostStateLifetimeAwait(t, old.Done())
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled retirement lost context identity: %v", err)
	}
	fresh, releaseFresh, err := life.enterTransport(t.Context())
	if err != nil {
		t.Fatal("cancelled retirement did not restore future genuine admission:", err)
	}
	defer releaseFresh()
	if fresh.Err() != nil || !errors.Is(old.Err(), context.Canceled) {
		t.Fatal("new work cancellation context revived an old cancelled transport")
	}
}

func TestHostTransportActualH4TierWriteDoesNotJoinItsOwnParticipant(t *testing.T) {
	h := hostTransportSavedFixture(t)
	startup, cleanup, attempt, err := beginHostTransportObservation(t.Context(), h)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	before := attempt.observation.source.generation
	changed, err := noteHostID(startup, h, strings.Repeat("a", 32))
	if err != nil || changed {
		t.Fatalf("first actual H4 identity write self-joined or changed identity: changed=%v, %v", changed, err)
	}
	after, err := currentMembershipGeneration(t.Context())
	if err != nil || before != after {
		t.Fatalf("the actual tiers-only H4 writer rotated membership: %v", err)
	}
	c := generatedHostTransportConn(h, strings.Repeat("a", 32))
	if err := finishHostTransportObservation(startup, attempt, c); err != nil {
		t.Fatal(err)
	}
}

func TestHostMetadataReusesOnlyRealExclusiveScopeInsideItsMemberLease(t *testing.T) {
	h, owner, _, _ := hostObservationFixture(t)
	exclusive, end, err := holdHostMutationContext(owner, h.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer end()
	err = withHostMembershipContext(exclusive, func(locked context.Context) error {
		reused, release, err := holdHostMutationContext(locked, h.Name)
		if err != nil {
			return err
		}
		defer release()
		if reused.Value(hostMutationContextKey{}) != exclusive.Value(hostMutationContextKey{}) {
			t.Fatal("member callback fabricated another exclusive scope")
		}
		return withStateFile(reused, []string{"hosts", HostFileKey(h.Name)}, "removal.json", true, func([]byte, bool) ([]byte, error) {
			return []byte("owned nested local preparation"), nil
		})
	})
	if err != nil {
		t.Fatal("the actual exclusive/member scope self-joined local preparation:", err)
	}
	end()
	if _, release, err := holdHostMutationContext(context.WithoutCancel(exclusive), h.Name); err == nil {
		release()
		t.Fatal("an expired exclusive carrier revived metadata ownership")
	}
}

func TestHostTerminalDrainsActualTransportBeforeTakingMembership(t *testing.T) {
	h, owner, _, _ := hostObservationFixture(t)
	startup, cleanup, _, err := beginHostTransportObservation(owner, h)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := UpdateHosts(owner, HostChange{Kind: "forget while transport pending", Was: &h}); err != nil {
		t.Fatal(err)
	}
	oldTake := takeLock
	defer func() { takeLock = oldTake }()
	member := make(chan struct{}, 1)
	takeLock = func(file *os.File) error {
		if filepath.Base(file.Name()) == filepath.Base(hostsPath()+".lock") {
			select {
			case member <- struct{}{}:
			default:
			}
		}
		return oldTake(file)
	}
	ended := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		defer close(ended)
		result <- WithHostForgetCommittedContext(owner, h, func(context.Context) error { return nil })
	}()
	hostStateLifetimeAwait(t, startup.Done())
	select {
	case <-member:
		t.Fatal("terminal cleanup took member before its actual transport drained")
	default:
	}
	hostStateLifetimeStaysBlocked(t, ended)
	cleanup()
	hostStateLifetimeAwait(t, ended)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}
