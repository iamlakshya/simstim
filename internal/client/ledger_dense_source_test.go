package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Setup streams the original valid counterexample before the actual writer.
// Nested duplicate metadata is deliberately opaque and must survive exactly.
func writeDenseLedgerObjectFixture(t *testing.T, path, placement string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	write := func(text string) {
		t.Helper()
		if _, err := file.WriteString(text); err != nil {
			t.Fatal(err)
		}
	}
	unknown := func() {
		var padding [4096]byte
		for i := range padding {
			padding[i] = 'x'
		}
		for index := 127; index >= 0; index-- {
			write(fmt.Sprintf(`,"future%03d":{"opaque":"`, index))
			for range 5 {
				if _, err := file.Write(padding[:]); err != nil {
					t.Fatal(err)
				}
			}
			write(`","duplicate":1,"duplicate":2}`)
		}
	}
	write(`{"version":1,"grants":[{"host_name":"box","host_id":"` + strings.Repeat("a", 32) + `","kind":"ssh-agent","first_at":"2026-11-14T12:00:00Z","last_at":"2026-11-14T12:00:00Z","count":1`)
	if placement == "row" {
		unknown()
	}
	write(`}]`)
	if placement == "root" {
		unknown()
	}
	write(`}`)
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLedgerDenseObjectsUseActualSameModeAndPreserveOpaqueMetadata(t *testing.T) {
	for _, placement := range []string{"root", "row"} {
		t.Run(placement, func(t *testing.T) {
			h := h5GrantState(t)
			conn := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), "session.id")
			request := NewGrantRequest(false, false)
			ctx := WithGrantRequest(t.Context(), request)
			defer request.Close()
			owned, release, err := conn.beginGrant(ctx, "dense ledger fixture", true)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			mode, err := newAgentModeResources(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer mode.closeInactive()
			owned = context.WithValue(owned, agentLedgerModeKey{}, mode)
			path := filepath.Join(StateDir(), grantsFileName)
			writeDenseLedgerObjectFixture(t, path, placement)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			fullBefore, err := decodeGrantsFile(before)
			if err != nil {
				t.Fatal("the full reader rejected the valid dense fixture", err)
			}
			previous := afterStateFilePublish
			defer func() { afterStateFilePublish = previous }()
			afterStateFilePublish = func() {
				if mode.active != nil {
					t.Fatal("ledger index/slab borrow survived the actual rename")
				}
			}
			if err := RecordGrantContext(owned, h, conn.HostID(), Outbound{Kind: OutboundSSHAgent}); err != nil {
				t.Fatal(err)
			}
			if len(mode.workspace) == 0 || mode.active != nil {
				t.Fatal("actual same-mode metadata lease was not used and joined")
			}
			slab := &mode.workspace[0]
			if err := RecordGrantContext(owned, h, conn.HostID(), Outbound{Kind: OutboundSSHAgent}); err != nil {
				t.Fatal(err)
			}
			if &mode.workspace[0] != slab {
				t.Fatal("ledger write created another metadata slab")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			fullAfter, err := decodeGrantsFile(after)
			if err != nil || len(fullAfter.Grants) != 1 || fullAfter.Grants[0].Count != 3 {
				t.Fatal("actual FD writer disagrees with the typed ledger count", err)
			}
			var original, current map[string]json.RawMessage
			if placement == "root" {
				original, current = fullBefore.unknown, fullAfter.unknown
			} else {
				original, current = fullBefore.Grants[0].unknown, fullAfter.Grants[0].unknown
			}
			for name, raw := range original {
				if !bytes.Equal(raw, current[name]) {
					t.Fatal("opaque dense metadata was rewritten", name)
				}
			}
			var old, next map[string]json.RawMessage
			if placement == "root" {
				old, next = fullBefore.unknown, fullAfter.unknown
			} else {
				old, next = fullBefore.Grants[0].unknown, fullAfter.Grants[0].unknown
			}
			for name, value := range old {
				if !bytes.Equal(value, next[name]) {
					t.Fatal("opaque unknown metadata was changed", name)
				}
			}
		})
	}
}

func TestDenseLedgerDuplicateRefusesWithoutPublishingOrKeepingIndex(t *testing.T) {
	h := h5GrantState(t)
	conn := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), "session.id")
	request := NewGrantRequest(false, false)
	ctx := WithGrantRequest(t.Context(), request)
	defer request.Close()
	owned, release, err := conn.beginGrant(ctx, "dense ledger duplicate fixture", true)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	mode, err := newAgentModeResources(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer mode.closeInactive()
	owned = context.WithValue(owned, agentLedgerModeKey{}, mode)
	path := filepath.Join(StateDir(), grantsFileName)
	writeDenseLedgerObjectFixture(t, path, "root")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Keep >64 distinct fields, then repeat one exact decoded name. The
	// actual private part may be attempted, but the live file must not move.
	before = append(bytes.TrimSuffix(before, []byte("}")), []byte(`,"future127":0}`)...)
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeGrantsFile(before); err == nil {
		t.Fatal("full reference accepted the duplicate fixture")
	}
	if err := RecordGrantContext(owned, h, conn.HostID(), Outbound{Kind: OutboundSSHAgent}); err == nil {
		t.Fatal("bounded FD writer accepted the duplicate")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) || mode.active != nil {
		t.Fatal("duplicate failure published or retained its metadata lease", err)
	}
}
