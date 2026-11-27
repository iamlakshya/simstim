package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestAuditReadKeepsNetworkEntriesAroundAnUnrelated200KiBLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	before := "{\"kind\":\"net\",\"host\":\"before.example.com\"}\n"
	other := "{\"kind\":\"output\",\"data\":\"" + strings.Repeat("x", 200<<10) + "\"}\n"
	after := "{\"kind\":\"net\",\"host\":\"after.example.com\"}\n"
	if err := os.WriteFile(path, []byte(before+other+after), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadAuditNetEntries(path, api.PolicyLogArgs{})
	if err != nil || len(got) != 2 || got[0].Host != "before.example.com" || got[1].Host != "after.example.com" {
		t.Fatalf("large unrelated audit line hid network history: entries=%+v error=%v", got, err)
	}
}
