package client

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
)

func TestPinMigrationCancelledBeforeWriteLeavesLegacyPin(t *testing.T) {
	h, _, before := legacyPinFixture(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := beforePinMigrationWrite
	beforePinMigrationWrite = func(string) { cancel() }
	t.Cleanup(func() { beforePinMigrationWrite = old })
	if err := migratePinContext(ctx, h.Name); !errors.Is(err, context.Canceled) {
		t.Fatalf("migration cancelled before write: %v", err)
	}
	after, err := os.ReadFile(legacyPinPath(h.Name))
	if err != nil || !bytes.Equal(before, after) || HasPin(h.Name) {
		t.Fatalf("migration wrote after cancellation: old=%q new pin=%v err=%v", after, HasPin(h.Name), err)
	}
}

func TestPinMigrationFinishesLegacyRemovalAfterCommittedWrite(t *testing.T) {
	h, key, _ := legacyPinFixture(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := pinMigrationWritten
	pinMigrationWritten = func(string) { cancel() }
	t.Cleanup(func() { pinMigrationWritten = old })
	if err := migratePinContext(ctx, h.Name); !errors.Is(err, context.Canceled) {
		t.Fatalf("migration cancelled after commit: %v", err)
	}
	got, err := readPin(h.Name)
	if err != nil || len(got) != 1 || got[0].Fingerprint() != key.Fingerprint() {
		t.Fatalf("committed pin was lost: %+v %v", got, err)
	}
	if _, err := os.Stat(legacyPinPath(h.Name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("committed migration stranded its legacy file: %v", err)
	}
}

func TestPinMigrationKeepsNonContextFailuresBestEffort(t *testing.T) {
	for _, kind := range []string{"corrupt", "foreign", "unsaved", "lock is a directory"} {
		t.Run(kind, func(t *testing.T) {
			h, key, before := legacyPinFixture(t, false)
			switch kind {
			case "corrupt":
				before = []byte("not a key\n")
			case "foreign":
				before = []byte(key.line(pinAlias("other")) + "\n")
			case "unsaved":
				if err := SaveHosts(context.Background(), nil); err != nil {
					t.Fatal(err)
				}
			case "lock is a directory":
				if err := os.Mkdir(lockFile(h.Name), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(legacyPinPath(h.Name), before, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := migratePinContext(context.Background(), h.Name); err != nil {
				t.Fatalf("non-context legacy failure stopped migration: %v", err)
			}
			after, err := os.ReadFile(legacyPinPath(h.Name))
			if err != nil || !bytes.Equal(before, after) || HasPin(h.Name) {
				t.Fatalf("best-effort migration changed its legacy file: %v", err)
			}
		})
	}
}
