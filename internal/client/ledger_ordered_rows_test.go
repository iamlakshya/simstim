package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestLedgerUnsortedRowsUseSameStoreAndStableFullOrdering(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(fmt.Sprint("selected=", present), func(t *testing.T) {
			h := h5GrantState(t)
			conn := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), "session.id")
			request := NewGrantRequest(false, false)
			ctx := WithGrantRequest(t.Context(), request)
			defer request.Close()
			owned, release, err := conn.beginGrant(ctx, "unsorted ledger fixture", true)
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
			first := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			file := grantsFile{Version: 1}
			for n := 256; n >= 0; n-- {
				row := GrantRecord{HostName: fmt.Sprintf("other%03d", n/2), Kind: OutboundPaste,
					FirstAt: first, LastAt: first, Count: n + 1,
					unknown: map[string]json.RawMessage{"opaque": json.RawMessage(fmt.Sprintf(`{"original":%d,"duplicate":1,"duplicate":2}`, n))}}
				file.Grants = append(file.Grants, row)
			}
			if present {
				file.Grants = append(file.Grants, GrantRecord{HostName: h.Name, HostID: conn.HostID(), Kind: OutboundSSHAgent, FirstAt: first, LastAt: first, Count: 7})
			}
			// The raw writer preserves the intentionally descending order.
			// The ordinary encoder would sort before the actual test starts.
			raw, err := encodeOpaqueGrantsFile(file)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(StateDir(), grantsFileName)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			expected, err := decodeGrantsFile(raw)
			if err != nil {
				t.Fatal(err)
			}
			slices.SortStableFunc(expected.Grants, compareLedgerRecords)
			if err := RecordGrantContext(owned, h, conn.HostID(), Outbound{Kind: OutboundSSHAgent}); err != nil {
				t.Fatal(err)
			}
			if mode.active != nil || len(mode.workspace) == 0 {
				t.Fatal("ordered rows retained their index or bypassed the actual mode")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := decodeGrantsFile(after)
			if err != nil {
				t.Fatal(err)
			}
			wantCount := len(expected.Grants)
			if !present {
				wantCount++
			}
			if len(actual.Grants) != wantCount {
				t.Fatal("ordered traversal lost a source row", len(actual.Grants), wantCount)
			}
			var oldRows []GrantRecord
			for i, row := range actual.Grants {
				if i > 0 && compareLedgerRecords(actual.Grants[i-1], row) > 0 {
					t.Fatal("FD ordering differs from the full ledger comparator", i)
				}
				if row.Kind == OutboundSSHAgent {
					want := 1
					if present {
						want = 8
					}
					if row.Count != want || row.HostName != h.Name || row.HostID != conn.HostID() {
						t.Fatal("selected/new agent row changed identity or count")
					}
					continue
				}
				oldRows = append(oldRows, row)
			}
			var at int
			for _, want := range expected.Grants {
				if want.Kind == OutboundSSHAgent {
					continue
				}
				got := oldRows[at]
				if got.HostName != want.HostName || got.Count != want.Count || !bytes.Equal(got.unknown["opaque"], want.unknown["opaque"]) {
					t.Fatal("stable tie order or opaque nested metadata changed", at)
				}
				at++
			}
		})
	}
}
