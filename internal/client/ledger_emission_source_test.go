package client

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLedgerCompletedEmissionRejectsKnownFailuresBesideOpaqueFutureMetadata(t *testing.T) {
	for _, known := range []string{`"count":0`, `"count":1,"Count":2`, `"count":1,"\u0063ount":2`} {
		t.Run(known, func(t *testing.T) {
			h := h5GrantState(t)
			conn := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), "session.id")
			request := NewGrantRequest(false, false)
			ctx := WithGrantRequest(t.Context(), request)
			defer request.Close()
			owned, release, err := conn.beginGrant(ctx, "completed ledger fixture", true)
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
			before := []byte(`{"version":1,"future":{"duplicate":1,"duplicate":2},"grants":[{"host_name":"box","host_id":"` + conn.HostID() +
				`","kind":"ssh-agent","first_at":"2026-11-14T12:00:00Z","last_at":"2026-11-14T12:00:00Z",` + known + `,"future":{"opaque":[1,2,3]}}]}`)
			if _, err := decodeGrantsFile(before); err == nil {
				t.Fatal("full semantic reference accepted the invalid known field")
			}
			path := filepath.Join(StateDir(), grantsFileName)
			if err := os.WriteFile(path, before, 0600); err != nil {
				t.Fatal(err)
			}
			if err := RecordGrantContext(owned, h, conn.HostID(), Outbound{Kind: OutboundSSHAgent}); err == nil {
				t.Fatal("opaque future metadata issued emission before complete known validation")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) || mode.active != nil {
				t.Fatal("known-field refusal published or retained an index", err)
			}
		})
	}
}
