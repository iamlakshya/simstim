package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/join/code"
	"github.com/iamlakshya/simstim/internal/proto"
)

func savedJoinedFixture(t *testing.T) Host {
	t.Helper()
	inviteHome(t)
	ctx := t.Context()
	inv, _, err := CreateHostInvite(ctx, InviteOptions{Name: "box", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	reply := code.Reply{Nonce: inv.Nonce, Account: "dev", Addresses: []string{"192.0.2.1"}, Port: 22, Fingerprints: []string{"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}}
	lease, err := BeginInviteAcceptance(ctx, "box", reply, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	h, stage, err := lease.StageSSHV1(lease.Context(), reply, reply.Addresses[0])
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Close()
	if err = lease.BindSSH(h, stage); err != nil {
		t.Fatal(err)
	}
	captured, err := lease.RegistrationContextV1()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = lease.RegistrationTargetV1(); err == nil {
		t.Fatal("an uncommitted capture manufactured a target")
	}
	staged, abort, err := StagePinOwned(captured, h.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer abort()
	pinStage, err := stageForContext(staged, h.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err = writePin(h.Name, pinStage, []hostKey{prepareTestHostKey(1)}); err != nil {
		t.Fatal(err)
	}
	change := HostChange{Kind: "host add", Now: &h, Pin: true, Tier: TierOwn, HostID: strings.Repeat("a", 32)}
	observation, err := ObserveHostRegistrationContext(staged, change)
	if err != nil {
		t.Fatal(err)
	}
	registration := WithHostStateObservation(staged, observation)
	registration, unlock, err := LockHost(registration, h.Name, "host add")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	transport := newPrepareBridgeTransport(t, change.HostID, nil, func(proto.Request, *proto.Writer) (any, *proto.Error, bool) {
		return nil, &proto.Error{Code: "unexpected", Message: "unexpected joined fixture operation"}, false
	})
	connection, err := dial(registration, h, route{t: transport})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err = UpdateHosts(registration, change); err != nil {
		t.Fatal(err)
	}
	target, err := lease.RegistrationTargetV1()
	if err != nil {
		t.Fatal(err)
	}
	if err = RecordInviteRegistrationV1(lease.Context(), target); err != nil {
		t.Fatal(err)
	}
	return target.Host()
}
func TestJoinedPhysicalHostRemovalUsesActualGenericWriter(t *testing.T) {
	h := savedJoinedFixture(t)
	directory := filepath.Dir(h.SSHConfig)
	if h.Machine != nil {
		t.Fatal("fixture accidentally used a session-machine permission")
	}
	removal, err := OpenSavedHostRemovalV1(t.Context(), h.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer removal.Close()
	if err = removal.Forget(removal.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("original joined directory survived: %v", err)
	}
	hosts, err := LoadHosts()
	if err != nil || len(hosts) != 0 {
		t.Fatalf("native forget did not commit: %v %v", hosts, err)
	}
}
func TestJoinedRemovalWriterFailureRestoresExactOriginalInode(t *testing.T) {
	h := savedJoinedFixture(t)
	before, err := os.Stat(h.SSHConfig)
	if err != nil {
		t.Fatal(err)
	}
	removal, err := OpenSavedHostRemovalV1(t.Context(), h.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer removal.Close()
	originalWriter := writeHosts
	t.Cleanup(func() { writeHosts = originalWriter })
	failure := errors.New("injected writer failure")
	writeHosts = func([]Host) error { return failure }
	if err = removal.Forget(removal.Context()); !errors.Is(err, failure) {
		t.Fatalf("writer failure: %v", err)
	}
	after, err := os.Stat(h.SSHConfig)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("rollback recreated or lost the original inode: %v", err)
	}
	saved, err := FindHost(h.Name)
	if err != nil || !SameEntry(saved, h) {
		t.Fatal("failed writer changed the saved Source")
	}
	writeHosts = originalWriter
	if err = removal.Retry(removal.Context()); err != nil {
		t.Fatal(err)
	}
}
func TestJoinedRollbackPreservesForeignDestinationAndRetainsOwner(t *testing.T) {
	h := savedJoinedFixture(t)
	removal, err := OpenSavedHostRemovalV1(t.Context(), h.Name)
	if err != nil {
		t.Fatal(err)
	}
	originalWriter := writeHosts
	t.Cleanup(func() { writeHosts = originalWriter })
	failure := errors.New("injected writer failure after foreign destination")
	writeHosts = func([]Host) error {
		if err := os.WriteFile(h.SSHConfig, []byte("foreign\n"), 0o600); err != nil {
			return err
		}
		return failure
	}
	err = removal.Forget(removal.Context())
	if !errors.Is(err, failure) {
		t.Fatalf("foreign destination result: %v", err)
	}
	if got, err := os.ReadFile(h.SSHConfig); err != nil || string(got) != "foreign\n" {
		t.Fatal("rollback overwrote the foreign destination")
	}
	if err = removal.Close(); err == nil {
		t.Fatal("incomplete rollback released its actual owner")
	}
	if _, err = OpenSavedHostRemovalV1(context.Background(), h.Name); err == nil {
		t.Fatal("another producer adopted the still-owned host")
	}
	if err = os.Remove(h.SSHConfig); err != nil {
		t.Fatal(err)
	}
	writeHosts = originalWriter
	if err = removal.Retry(removal.Context()); err != nil {
		t.Fatal(err)
	}
	if err = removal.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOrdinaryHostWithoutJoinedOwnerKeepsItsRemovalBehaviour(t *testing.T) {
	inviteHome(t)
	h := Host{Name: "ordinary", SSH: "ordinary.example.com"}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	removal, err := OpenSavedHostRemovalV1(t.Context(), h.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer removal.Close()
	if err = removal.Forget(removal.Context()); err != nil {
		t.Fatal(err)
	}
	hosts, err := LoadHosts()
	if err != nil || len(hosts) != 0 {
		t.Fatal("joined participant changed ordinary host removal")
	}
}
