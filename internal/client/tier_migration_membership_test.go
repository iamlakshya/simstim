package client

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestTierMigrationUsesTheLockedSavedList(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "removed and added", true: "unreadable current list"}[corrupt], func(t *testing.T) {
			ownLockFiles(t, time.Millisecond, time.Second)
			stale := []Host{{Name: "old", SSH: "old.example"}}
			if err := SaveHosts(context.Background(), stale); err != nil {
				t.Fatal(err)
			}
			actual := []Host{{Name: "new", SSH: "new.example"}}
			oldTake := takeLock
			changed := false
			takeLock = func(f *os.File) error {
				if f.Name() == hostsPath()+".lock" && !changed {
					changed = true
					if corrupt {
						if err := os.WriteFile(hostsPath(), []byte("[broken"), 0o600); err != nil {
							return err
						}
					} else if err := writeHosts(actual); err != nil {
						return err
					}
				}
				return oldTake(f)
			}
			t.Cleanup(func() { takeLock = oldTake })
			ts, err := LoadTiersContext(context.Background(), stale)
			if !changed {
				t.Fatal("migration did not lock the saved host list")
			}
			if corrupt {
				var user *UserError
				if !errors.As(err, &user) || user.Code != "bad_config" {
					t.Fatalf("migration ignored current hosts error: %v", err)
				}
				if _, err := os.Lstat(tiersPath()); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("invalid saved list created a migration: %v", err)
				}
				return
			}
			if err != nil || ts.Has("old") || !ts.Has("new") || ts.Of("new") != TierOwn {
				t.Fatalf("migration kept stale membership: old=%v new=%v tier=%s err=%v", ts.Has("old"), ts.Has("new"), ts.Of("new"), err)
			}
		})
	}
}

func TestTierMigrationHoldsBothFileLocksThroughPublication(t *testing.T) {
	ownLockFiles(t, time.Millisecond, time.Second)
	h := Host{Name: "box", SSH: "box.example"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	old := beforeTierMigrationSave
	checked := false
	beforeTierMigrationSave = func() {
		checked = true
		assertHostFileLockHeld(t, hostsPath()+".lock")
		assertHostFileLockHeld(t, tiersPath()+".lock")
	}
	t.Cleanup(func() { beforeTierMigrationSave = old })
	if _, err := LoadTiersContext(context.Background(), []Host{h}); err != nil || !checked {
		t.Fatalf("migration publication: checked=%v err=%v", checked, err)
	}
}
