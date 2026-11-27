package client

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLocalPurposeLedgerFullFDParity(t *testing.T) {
	for _, test := range []struct {
		kind OutboundKind
		name string
		ok   bool
	}{{OutboundPush, "github.com/Owner/repo.git", true}, {OutboundPublicPush, "github.com/Owner/repo.git", true},
		{OutboundRootRun, strings.Repeat("a", 64), true}, {OutboundRootRun, strings.Repeat("A", 64), false},
		{OutboundPush, "github.com/Owner/*", false}, {OutboundPublicPush, "https://github.com/Owner/repo", false},
		{OutboundRootRun, "", false}} {
		t.Run(string(test.kind)+"/"+test.name, func(t *testing.T) {
			record := GrantRecord{HostName: "box", HostID: strings.Repeat("b", 32), Kind: test.kind, Name: test.name,
				FirstAt: time.Unix(1, 0).UTC(), LastAt: time.Unix(2, 0).UTC(), Count: 2}
			raw, err := json.Marshal(grantsFile{Version: 1, Grants: []GrantRecord{record}})
			if err != nil {
				t.Fatal(err)
			}
			full, fullErr := decodeGrantsFile(raw)
			fd, fdErr := projectLedger(t.Context(), bytes.NewReader(raw), int64(len(raw)), Host{Name: "box"}, record.HostID)
			if (fullErr == nil) != test.ok || (fdErr == nil) != test.ok {
				t.Fatalf("typed/FD ledger validity differs: full=%v fd=%v want=%v", fullErr, fdErr, test.ok)
			}
			if !test.ok {
				return
			}
			if len(full.Grants) != 1 || full.Grants[0].Name != test.name || fd.count != 1 {
				t.Fatal("the canonical suffix or distinct ledger row was changed")
			}
		})
	}
	if grantKindRank(OutboundPush) == grantKindRank(OutboundPublicPush) || grantKindRank(OutboundPush) == grantKindRank(OutboundRepo) {
		t.Fatal("push consent kinds share another ledger's ordering identity")
	}
}
