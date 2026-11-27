package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRootHistoryChecksNestedAliasesAndKeepsUnknownConservative(t *testing.T) {
	for _, test := range []struct {
		name      string
		change    func(string) string
		wantUsed  bool
		wantError bool
	}{{"ordinary unused history", func(s string) string { return s }, false, false},
		{"record alias", func(s string) string {
			return strings.Replace(s, `"started_seen":false`, `"started_seen":false,"Started_seen":true`, 1)
		}, false, true},
		{"record duplicate", func(s string) string {
			return strings.Replace(s, `"started_seen":false`, `"started_seen":false,"started_seen":true`, 1)
		}, false, true},
		{"unknown record", func(s string) string {
			return strings.Replace(s, `"started_seen":false`, `"started_seen":false,"future_start_fact":true`, 1)
		}, true, false},
		{"unknown root", func(s string) string { return strings.TrimSuffix(s, "}") + `,"future_start_fact":true}` }, true, false},
		{"duplicate sticky identity", func(s string) string {
			return strings.Replace(s, `"started":{}`, `"started":{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa":true,"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa":true}`, 1)
		}, false, true}} {
		t.Run(test.name, func(t *testing.T) {
			h := h5GrantState(t)
			record := RootStepRecord{HostID: strings.Repeat("a", 32), Steps: []RootStepID{RootLinger}, SHA256: strings.Repeat("b", 64), Mode: RootPrint, At: time.Unix(1, 0).UTC(), Status: "printed"}
			encoded, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			// Spell the optional false fact explicitly so aliases and exact
			// duplicates exercise the actual per-record object boundary.
			body := `{"version":1,"host":"box","records":[` + strings.TrimSuffix(string(encoded), "}") + `,"started_seen":false}],"started":{}}`
			path := filepath.Join(StateDir(), "hosts", HostFileKey(h.Name), "root-steps.json")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(test.change(body)), 0600); err != nil {
				t.Fatal(err)
			}
			used, err := RootHistoryStarted(t.Context(), h, record.HostID)
			if (err != nil) != test.wantError || err == nil && used != test.wantUsed {
				t.Fatalf("history used=%v err=%v, want used=%v error=%v", used, err, test.wantUsed, test.wantError)
			}
		})
	}
}
