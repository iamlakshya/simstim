//go:build linux

package publicationlease

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

func publicationRootV1(t *testing.T) (*os.Root, cachefs.Domain, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := cachefs.OpenPrivateRoot(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root, nativeFixtureDomainV1(), strings.Repeat("c", 32)
}
func nativeFixtureDomainV1() cachefs.Domain {
	return cachefs.Domain{Scope: strings.Repeat("a", 64), HostID: strings.Repeat("b", 32)}
}

func TestPublicationReservesUnnamedIdentityBeforeAnyNamedFenceV1(t *testing.T) {
	root, domain, token := publicationRootV1(t)
	var reserved string
	source, err := CreateReservedV1(t.Context(), root, domain, token, func(encoded string) error {
		record, err := DecodeRecordV1(encoded)
		if err != nil {
			return err
		}
		if record.Domain != domain || record.Token != token || !record.Self.Known() {
			t.Fatal("reservation lost its original native identity")
		}
		if _, err := root.Lstat(nameV1(token)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("fence named before reservation", err)
		}
		reserved = encoded
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if source.RecordV1() != reserved || reserved == "" {
		t.Fatal("factory replaced its reserved identity")
	}
	if err := source.CheckV1(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := source.FreezeParentV1(); err != nil {
		t.Fatal(err)
	}
	proof, err := source.ProbeV1(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := proof.RemoveV1(); err != nil {
		t.Fatal(err)
	}
	if err := source.CloseV1(); err != nil {
		t.Fatal(err)
	}
}

func TestPublicationFreshEXWaitsForEveryOriginalDescriptionCopyV1(t *testing.T) {
	root, domain, token := publicationRootV1(t)
	source, err := CreateV1(t.Context(), root, domain, token)
	if err != nil {
		t.Fatal(err)
	}
	first, err := source.RetainForHelperV1(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := duplicateV1(first, "original-next-helper")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	defer second.Close()
	if err := source.FreezeParentV1(); err != nil {
		t.Fatal(err)
	}
	if _, err := source.RetainForHelperV1(t.Context()); err == nil {
		t.Fatal("frozen parent issued another loan")
	}
	if _, err := source.ProbeV1(t.Context()); !errors.Is(err, ErrBusy) {
		t.Fatal("live helper lost its fence", err)
	}
	first.Close()
	if _, err := source.ProbeV1(t.Context()); !errors.Is(err, ErrBusy) {
		t.Fatal("next helper lost its same inherited OFD", err)
	}
	if err := source.CloseV1(); !errors.Is(err, ErrBusy) {
		t.Fatal("unknown closure released the original owner", err)
	}
	second.Close()
	proof, err := source.ProbeV1(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := proof.RemoveV1(); err != nil {
		t.Fatal(err)
	}
	if err := source.CloseV1(); err != nil {
		t.Fatal(err)
	}
}

func TestPublicationDataCheckNeverChangesInheritedLockV1(t *testing.T) {
	root, domain, token := publicationRootV1(t)
	source, err := CreateV1(t.Context(), root, domain, token)
	if err != nil {
		t.Fatal(err)
	}
	if held, err := cachefs.TryLock(source.lock, cachefs.Exclusive); err != nil || !held {
		t.Fatal("fixture EX", err)
	}
	if err := CheckInheritedV1(source.lock, source.RecordV1()); err != nil {
		t.Fatal(err)
	}
	other, err := cachefs.OpenFile(root, nameV1(token), os.O_RDONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if held, err := cachefs.TryLock(other, cachefs.Shared); err != nil || held {
		t.Fatal("data check downgraded the inherited original EX", err)
	}
	if err := source.FreezeParentV1(); err != nil {
		t.Fatal(err)
	}
	proof, err := source.ProbeV1(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := proof.RemoveV1(); err != nil {
		t.Fatal(err)
	}
	if err := source.CloseV1(); err != nil {
		t.Fatal(err)
	}
}

func TestPublicationFailedReservationRetainsOnlyOriginalUnnamedCreatorV1(t *testing.T) {
	root, domain, token := publicationRootV1(t)
	stopped := errors.New("canonical reservation write failed")
	source, err := CreateReservedV1(t.Context(), root, domain, token, func(string) error { return stopped })
	if !errors.Is(err, stopped) || source == nil {
		t.Fatal("failed factory lost its original creator", err)
	}
	if _, err := source.RetainForHelperV1(t.Context()); err == nil {
		t.Fatal("failed factory lent a helper")
	}
	if _, err := root.Lstat(nameV1(token)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed reservation published a name", err)
	}
	if err := source.CloseUnpublishedV1(); err != nil {
		t.Fatal(err)
	}
	if err := source.CloseUnpublishedV1(); err != nil {
		t.Fatal(err)
	}
	if err := source.CloseV1(); err != nil {
		t.Fatal(err)
	}
}

func TestPublicationRetirementResumesAfterActualUnlinkV1(t *testing.T) {
	root, domain, token := publicationRootV1(t)
	source, err := CreateV1(t.Context(), root, domain, token)
	if err != nil {
		t.Fatal(err)
	}
	encoded := source.RecordV1()
	if err := source.FreezeParentV1(); err != nil {
		t.Fatal(err)
	}
	proof, err := source.ProbeV1(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// The host persists its exact removing intent while this EX is held.
	if err := proof.RemoveV1(); err != nil {
		t.Fatal(err)
	}
	if err := source.CloseV1(); err != nil {
		t.Fatal(err)
	}
	if _, err := TryExclusiveV1(t.Context(), root, domain, encoded); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing live fence became closure", err)
	}
	resumed, err := ResumeRemovalV1(context.Background(), root, domain, encoded)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.CloseV1()
	if err := resumed.RemoveV1(); err != nil {
		t.Fatal(err)
	}
	foreign := domain
	foreign.HostID = strings.Repeat("d", 32)
	if _, err := ResumeRemovalV1(t.Context(), root, foreign, encoded); err == nil {
		t.Fatal("foreign intent adopted missing leaf")
	}
}
