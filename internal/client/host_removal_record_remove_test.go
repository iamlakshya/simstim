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

// Generated local data exercises only backend snapshot/unlink mechanics.
// H15 tests the actual closed Version0/1 destroy-only receipt eligibility.
func hostPlainForgetRecordFixture(t *testing.T) (Host, context.Context, []byte) {
	t.Helper()
	h, ctx, _, _ := hostObservationFixture(t)
	data := []byte("generated closed destroy-only fixture")
	stateFDWriteContext(t, ctx, []string{"hosts", HostFileKey(h.Name)}, "removal.json", data)
	stateFDWriteContext(t, ctx, []string{"hosts", HostFileKey(h.Name)}, "baseline.json", []byte("other history stays"))
	if err := UpdateHosts(ctx, HostChange{Kind: "plain forget fixture", Was: &h}); err != nil {
		t.Fatal(err)
	}
	return h, ctx, data
}

func TestHostRemovalRecordClearUsesExactVerifiedOriginalAndRevokesView(t *testing.T) {
	h, ctx, expected := hostPlainForgetRecordFixture(t)
	var escaped io.ReaderAt
	removed := false
	err := WithHostForgetCommittedContext(ctx, h, func(verified context.Context) error {
		var err error
		removed, err = removeHostRemovalRecordContext(verified, h, func(view stateFileView) error {
			escaped = view.ReaderAt
			data, err := io.ReadAll(io.NewSectionReader(view.ReaderAt, 0, view.Size))
			if err != nil || !bytes.Equal(data, expected) {
				return errors.New("generated owner refused")
			}
			return nil
		})
		return err
	})
	if err != nil || !removed {
		t.Fatalf("same original verified record did not clear: removed=%v, %v", removed, err)
	}
	if _, err := escaped.ReadAt(make([]byte, 1), 0); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("owner view escaped single-record revocation: %v", err)
	}
	terminalAssertAbsent(t, filepath.Join(removalDir(h.Name), "removal.json"))
	for _, path := range []string{removalDir(h.Name), filepath.Join(removalDir(h.Name), "removal.json.lock"), hostLockPath(h.Name)} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("single-record clear retired an unrequested domain: %s, %v", path, err)
		}
	}
	if got, err := os.ReadFile(filepath.Join(removalDir(h.Name), "baseline.json")); err != nil || string(got) != "other history stays" {
		t.Fatalf("single-record clear changed other history: %q, %v", got, err)
	}
}

func TestHostRemovalRecordClearRefusesUnverifiedWrongOriginalOwnerAndMissingLock(t *testing.T) {
	for _, kind := range []string{"unverified", "wrong-original", "owner", "missing-lock"} {
		t.Run(kind, func(t *testing.T) {
			h, ctx, expected := hostPlainForgetRecordFixture(t)
			path := filepath.Join(removalDir(h.Name), "removal.json")
			if kind == "missing-lock" {
				if err := os.Remove(path + ".lock"); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			marker := errors.New("canonical owner refused unknown or purge data")
			work := func(verified context.Context) error {
				original := h
				if kind == "wrong-original" {
					original.SSH = "another-original.example"
				}
				removed, err := removeHostRemovalRecordContext(verified, original, func(stateFileView) error { called = true; return marker })
				if removed || err == nil || kind != "owner" && called {
					t.Fatalf("refused clearing reached mutation: removed=%v, called=%v, %v", removed, called, err)
				}
				if kind == "owner" && err != marker {
					t.Fatalf("pure owner error identity changed: %v", err)
				}
				return nil
			}
			if kind == "unverified" {
				if err := work(ctx); err != nil {
					t.Fatal(err)
				}
			} else if err := WithHostForgetCommittedContext(ctx, h, work); err != nil {
				t.Fatal(err)
			}
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, expected) {
				t.Fatalf("refused clear changed the journal: %v", err)
			}
			if kind == "missing-lock" {
				terminalAssertAbsent(t, path+".lock")
			}
		})
	}
}

func TestHostRemovalRecordClearReportsCommittedUnlinkAfterCancellation(t *testing.T) {
	h, owner, _ := hostPlainForgetRecordFixture(t)
	ctx, cancel := context.WithCancel(owner)
	defer cancel()
	path := filepath.Join(removalDir(h.Name), "removal.json")
	old := stateValidateDescriptor
	defer func() { stateValidateDescriptor = old }()
	stateValidateDescriptor = func(file *os.File, mode os.FileMode) (os.FileInfo, error) {
		if filepath.Base(file.Name()) == "removal.json.lock" {
			if _, err := os.Lstat(path); os.IsNotExist(err) {
				cancel()
			}
		}
		return old(file, mode)
	}
	removed := false
	err := WithHostForgetCommittedContext(ctx, h, func(verified context.Context) error {
		var err error
		removed, err = removeHostRemovalRecordContext(verified, h, func(stateFileView) error { return nil })
		return err
	})
	if !removed || !errors.Is(err, context.Canceled) {
		t.Fatalf("committed unlink/cancellation was hidden: removed=%v, %v", removed, err)
	}
	terminalAssertAbsent(t, path)
}
