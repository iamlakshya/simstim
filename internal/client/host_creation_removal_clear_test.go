package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func creationJournalBackendFixture(t *testing.T) (HostCreationRequest, context.Context, func(), []byte) {
	t.Helper()
	stateFDTestHome(t)
	request := hostCreationRequestFixture()
	folder := removalDir(request.Name)
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("generated completed destroy-only owner fixture")
	for name, raw := range map[string][]byte{"removal.json": data, "removal.json.lock": nil, "baseline.json": []byte("other history stays")} {
		if err := os.WriteFile(filepath.Join(folder, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, release, err := BeginHostCreationContext(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	return request, ctx, release, data
}

func generatedCreationJournalValidator(expected []byte) func(stateFileView) error {
	return func(view stateFileView) error {
		actual, err := io.ReadAll(io.NewSectionReader(view.ReaderAt, 0, view.Size))
		if err != nil {
			return err
		}
		if !view.Exists || !bytes.Equal(actual, expected) {
			return errors.New("generated owner refused")
		}
		return nil
	}
}

func TestHostCreationJournalClearPublishesNewAbsentDataProofWithOriginalDomains(t *testing.T) {
	request, ctx, _, data := creationJournalBackendFixture(t)
	if err := withHostCreationRemovalInspection(ctx, request.Name, generatedCreationJournalValidator(data)); err != nil {
		t.Fatal(err)
	}
	o := ctx.Value(hostStateObservationKey{}).(*HostStateObservation)
	before := o.creation.preflight
	var escaped io.ReaderAt
	removed, err := clearHostCreationRemovalRecordContext(ctx, request.Name, func(view stateFileView) error {
		escaped = view.ReaderAt
		return generatedCreationJournalValidator(data)(view)
	})
	if err != nil || !removed {
		t.Fatalf("exact creation record did not clear: removed=%v, %v", removed, err)
	}
	after := o.creation.preflight
	if after == nil || after == before || after.data != nil || after.root == nil || after.lock == nil ||
		after.root.Identity != before.root.Identity || !after.root.Modified.Equal(before.root.Modified) ||
		after.lock.Identity != before.lock.Identity || !after.lock.Modified.Equal(before.lock.Modified) {
		t.Fatal("clearing kept old data proof or replaced original root/sentinel references")
	}
	if _, err := escaped.ReadAt(make([]byte, 1), 0); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("owner view escaped clearing lifetime: %v", err)
	}
	terminalAssertAbsent(t, filepath.Join(removalDir(request.Name), "removal.json"))
	for _, path := range []string{removalDir(request.Name), filepath.Join(removalDir(request.Name), "removal.json.lock"), hostLockPath(request.Name)} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("fixed clearing retired a retained domain: %s, %v", path, err)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(removalDir(request.Name), "baseline.json")); err != nil || string(raw) != "other history stays" {
		t.Fatalf("creation clearing changed other history: %q, %v", raw, err)
	}
	if err := StartHostCreationContext(ctx); err != nil {
		t.Fatal("new absent-data proof could not pass actual Start verification:", err)
	}
}

func TestHostCreationJournalClearFailureDoesNotKeepPriorSuccessfulPreflight(t *testing.T) {
	for _, failureAt := range []string{"owner", "removal.json", "removal.json.lock", "state-root"} {
		t.Run(failureAt, func(t *testing.T) {
			request, ctx, _, data := creationJournalBackendFixture(t)
			if err := withHostCreationRemovalInspection(ctx, request.Name, generatedCreationJournalValidator(data)); err != nil {
				t.Fatal(err)
			}
			o := ctx.Value(hostStateObservationKey{}).(*HostStateObservation)
			if o.creation.preflight == nil {
				t.Fatal("initial successful proof fixture missing")
			}
			marker := errors.New("scoped owned close/owner failure")
			observed := false
			active := context.WithValue(ctx, stateOwnedCloseKey{}, func(name string) error {
				if name == failureAt {
					observed = true
					return marker
				}
				return nil
			})
			removed, err := clearHostCreationRemovalRecordContext(active, request.Name, func(view stateFileView) error {
				if failureAt == "owner" {
					observed = true
					return marker
				}
				return generatedCreationJournalValidator(data)(view)
			})
			if err == nil || !observed || removed != (failureAt != "owner") || o.creation.preflight != nil {
				t.Fatalf("failed finalisation published/kept proof: removed=%v, observed=%v, proof=%v, %v", removed, observed, o.creation.preflight, err)
			}
			if err := StartHostCreationContext(ctx); err == nil {
				t.Fatal("failed close/owner validation permitted provider start")
			}
			if failureAt == "owner" && err != marker {
				t.Fatalf("primary owner error identity changed: %v", err)
			}
		})
	}
}

func TestHostCreationJournalClearRefusesMissingSentinelAndExpiredReservation(t *testing.T) {
	for _, kind := range []string{"missing-lock", "ended-owner", "changed-source", "started"} {
		t.Run(kind, func(t *testing.T) {
			request, ctx, release, data := creationJournalBackendFixture(t)
			path := filepath.Join(removalDir(request.Name), "removal.json")
			switch kind {
			case "missing-lock":
				if err := os.Remove(path + ".lock"); err != nil {
					t.Fatal(err)
				}
			case "ended-owner":
				release()
			case "changed-source":
				if err := SaveHosts(t.Context(), nil); err != nil {
					t.Fatal(err)
				}
			case "started":
				if err := withHostCreationRemovalInspection(ctx, request.Name, generatedCreationJournalValidator(data)); err != nil {
					t.Fatal(err)
				}
				if err := StartHostCreationContext(ctx); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			removed, err := clearHostCreationRemovalRecordContext(ctx, request.Name, func(stateFileView) error { called = true; return nil })
			if err == nil || removed || called {
				t.Fatalf("invalid creation scope reached owner/deletion: removed=%v, called=%v, %v", removed, called, err)
			}
			if raw, err := os.ReadFile(path); err != nil || !bytes.Equal(raw, data) {
				t.Fatalf("refusal changed journal: %v", err)
			}
			if kind == "missing-lock" {
				terminalAssertAbsent(t, path+".lock")
			}
		})
	}
}

func TestHostCreationJournalClearCancellationAfterUnlinkCannotPublishProof(t *testing.T) {
	request, owner, _, data := creationJournalBackendFixture(t)
	ctx, cancel := context.WithCancel(owner)
	defer cancel()
	ctx = context.WithValue(ctx, stateOwnedCloseKey{}, func(name string) error {
		if name == "removal.json" {
			cancel()
		}
		return nil
	})
	removed, err := clearHostCreationRemovalRecordContext(ctx, request.Name, generatedCreationJournalValidator(data))
	o := owner.Value(hostStateObservationKey{}).(*HostStateObservation)
	if !removed || !errors.Is(err, context.Canceled) || o.creation.preflight != nil {
		t.Fatalf("committed cancellation published successful absence: removed=%v, proof=%v, %v", removed, o.creation.preflight, err)
	}
	if err := StartHostCreationContext(owner); err == nil {
		t.Fatal("cancelled clearing permitted a provider start")
	}
}

func TestHostCreationJournalReappearingAfterClearRefusesStart(t *testing.T) {
	request, ctx, _, data := creationJournalBackendFixture(t)
	if removed, err := clearHostCreationRemovalRecordContext(ctx, request.Name, generatedCreationJournalValidator(data)); err != nil || !removed {
		t.Fatalf("clear fixture failed: removed=%v, %v", removed, err)
	}
	path := filepath.Join(removalDir(request.Name), "removal.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := StartHostCreationContext(ctx); err == nil {
		t.Fatal("recorded-absent journal appearance was adopted before provider start")
	}
	if raw, err := os.ReadFile(path); err != nil || !bytes.Equal(raw, data) {
		t.Fatalf("refused Start changed reappeared record: %v", err)
	}
}
