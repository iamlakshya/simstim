package client

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLedgerSpanEmissionPreservesOpaqueBytesAcrossKnownFieldOrders(t *testing.T) {
	for _, order := range []string{"count-first", "time-first", "escaped-count"} {
		t.Run(order, func(t *testing.T) {
			h := h5GrantState(t)
			conn := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), "session.id")
			request := NewGrantRequest(false, false)
			ctx := WithGrantRequest(t.Context(), request)
			defer request.Close()
			owned, release, err := conn.beginGrant(ctx, "ledger span fixture", true)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			count := `"count":1`
			if order == "escaped-count" {
				count = `"\u0063ount":1`
			}
			times := `"last_at":"2026-11-14T12:00:00Z", "first_at":"2026-11-14T12:00:00Z"`
			fields := count + `, "opaque_between":{ "duplicate":1, "duplicate":2 }, ` + times
			if order == "time-first" {
				fields = times + `, "opaque_between":{ "duplicate":1, "duplicate":2 }, ` + count
			}
			before := []byte(`{ "opaque_root":{ "duplicate":1, "duplicate":2 }, "version":1,"grants":[ {` + fields +
				`,"host_name":"box","host_id":"` + conn.HostID() + `","kind":"ssh-agent","opaque_tail":[ 1, 2, 3 ]} ] }`)
			fullBefore, err := decodeGrantsFile(before)
			if err != nil {
				t.Fatal("full reference refused a legal original field order", err)
			}
			path := filepath.Join(StateDir(), grantsFileName)
			if err := os.WriteFile(path, before, 0600); err != nil {
				t.Fatal(err)
			}
			if err := RecordGrantContext(owned, h, conn.HostID(), Outbound{Kind: OutboundSSHAgent}); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			fullAfter, err := decodeGrantsFile(after)
			if err != nil || len(fullAfter.Grants) != 1 || fullAfter.Grants[0].Count != 2 {
				t.Fatal("known value spans were spliced out of order or decoded incorrectly", err)
			}
			for name, value := range fullBefore.unknown {
				if !bytes.Equal(value, fullAfter.unknown[name]) {
					t.Fatal("root opaque metadata changed", name)
				}
			}
			for name, value := range fullBefore.Grants[0].unknown {
				if !bytes.Equal(value, fullAfter.Grants[0].unknown[name]) {
					t.Fatal("row opaque metadata changed", name)
				}
			}
		})
	}
}
