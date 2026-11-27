package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestHostBulkCannotRemoveOrRemapBetweenActualLedgerAndFirstFrame(t *testing.T) {
	for _, remap := range []bool{false, true} {
		t.Run(map[bool]string{false: "remove", true: "remap"}[remap], func(t *testing.T) {
			h, owner, _, unlock := hostObservationFixture(t)
			ctx, cancel := context.WithCancel(owner)
			defer cancel()
			ctx, unpin, err := holdHostPublicationContext(ctx, h.Name)
			if err != nil {
				t.Fatal(err)
			}
			var once sync.Once
			release := func() { once.Do(func() { unpin(); unlock() }) }
			defer release()
			id := strings.Repeat("a", 32)
			if err := RecordGrantContext(ctx, h, id, Outbound{Kind: OutboundSecret, Name: "API_KEY"}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(hostsPath())
			if err != nil {
				t.Fatal(err)
			}
			published := false
			c := &Conn{Host: h, pending: map[uint64]chan proto.Response{}, done: make(chan struct{})}
			c.w = proto.NewWriter(grantWriteFunc(func(p []byte) (int, error) {
				published = true
				rows, err := GrantsForHostContext(t.Context(), h, id)
				if err != nil || len(rows) != 1 || rows[0].Name != "API_KEY" {
					t.Fatalf("the actual frame preceded its durable ledger row: %+v, %v", rows, err)
				}
				var desired []Host
				if remap {
					changed := cloneObservationHost(h)
					changed.SSH = "replacement.example"
					desired = []Host{changed}
				}
				var user *UserError
				if err := SaveHosts(t.Context(), desired); !errors.As(err, &user) || user.Code != "host_busy" {
					t.Fatalf("bulk changed the grant target during actual first-frame publication: %v", err)
				}
				if got, err := os.ReadFile(hostsPath()); err != nil || !bytes.Equal(got, before) {
					t.Fatalf("busy bulk publication changed hosts bytes: %v", err)
				}
				cancel()
				return len(p), nil
			}))
			if err := c.callWithSent(ctx, "bulk-order-fixture", nil, nil, release); !published || !errors.Is(err, context.Canceled) {
				t.Fatalf("the actual call-with-sent ordering fixture failed: published=%v, %v", published, err)
			}
		})
	}
}

func TestHostBulkIdenticalAndUnrelatedWritesDoNotProbeUnchangedOwner(t *testing.T) {
	for _, unrelated := range []bool{false, true} {
		t.Run(map[bool]string{false: "identical", true: "unrelated"}[unrelated], func(t *testing.T) {
			h, _, _, _ := hostObservationFixture(t)
			before, err := currentMembershipGeneration(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			desired := []Host{h}
			if unrelated {
				desired = append(desired, Host{Name: "other", SSH: "other.example"})
			}
			if err := SaveHosts(t.Context(), desired); err != nil {
				t.Fatal("bulk probed an unchanged active target:", err)
			}
			after, err := currentMembershipGeneration(t.Context())
			if err != nil || before == after {
				t.Fatalf("the canonical actual bulk write did not rotate generation: %v", err)
			}
		})
	}
}

func TestHostBulkLargeChangedListClosesEachActualProbeBeforeNext(t *testing.T) {
	stateFDTestHome(t)
	const count = 1024
	old, next := make([]Host, count), make([]Host, count)
	for i := range old {
		old[i] = Host{Name: fmt.Sprintf("host-%04d", i), SSH: "before.example"}
		next[i] = Host{Name: old[i].Name, SSH: "after.example"}
	}
	if err := SaveHosts(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(StateDir(), "hosts"), 0700); err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool, count)
	for _, h := range old {
		path := hostLockPath(h.Name)
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
		names[filepath.Base(path)] = true
	}
	originalTake := takeLock
	defer func() { takeLock = originalTake }()
	var previous *os.File
	probes := 0
	takeLock = func(file *os.File) error {
		if names[filepath.Base(file.Name())] {
			if previous != nil {
				if _, err := previous.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Errorf("the previous actual OP probe remained open at the next probe: %v", err)
				}
			}
			previous = file
			probes++
		}
		return originalTake(file)
	}
	if err := SaveHosts(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	if probes != count || previous == nil {
		t.Fatalf("bulk did not probe the complete changed list: %d", probes)
	}
	if _, err := previous.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("the last actual OP probe escaped bulk completion: %v", err)
	}
}

func TestHostBulkDuplicateRowsCannotHideChangedActiveName(t *testing.T) {
	h, _, _, _ := hostObservationFixture(t)
	duplicate := cloneObservationHost(h)
	duplicate.SSH = "hidden.example"
	var user *UserError
	if err := SaveHosts(t.Context(), []Host{h, duplicate}); !errors.As(err, &user) || user.Code != "host_busy" {
		t.Fatalf("a last-map-value comparison hid the active duplicate name: %v", err)
	}
}

func TestHostBulkRefusesTerminalOPWithoutCreatingMissingNames(t *testing.T) {
	stateFDTestHome(t)
	h := Host{Name: "box", SSH: "before.example"}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(hostLockPath(h.Name)), 0700); err != nil {
		t.Fatal(err)
	}
	proof := []byte(hostTerminalPrefix + "generated incomplete checkpoint fixture")
	if err := os.WriteFile(hostLockPath(h.Name), proof, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(hostsPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveHosts(t.Context(), nil); err == nil {
		t.Fatal("bulk invalidated a terminal domain instead of preserving it")
	}
	if got, err := os.ReadFile(hostsPath()); err != nil || !bytes.Equal(got, before) {
		t.Fatalf("terminal refusal changed the saved host list: %v", err)
	}
	if got, err := os.ReadFile(hostLockPath(h.Name)); err != nil || !bytes.Equal(got, proof) {
		t.Fatalf("terminal refusal changed the OP checkpoint fixture: %v", err)
	}
	if _, err := os.Lstat(hostLockPath(h.Name) + ".who"); !os.IsNotExist(err) {
		t.Fatalf("a bulk probe created a diagnostic: %v", err)
	}
}
