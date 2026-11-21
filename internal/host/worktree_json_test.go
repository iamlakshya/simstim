package host

import (
	"encoding/json"
	"testing"
)

func TestWorktreeDescriptionRetainsUnknownResourceRoles(t *testing.T) {
	raw := []byte(`{"repo_root":"/repository","worktree":"/worktree","branch":"topic","base_ref":"HEAD","base_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","created_by_us":true,"future_resource":{"preserve":true}}`)
	var git GitInfo
	if err := json.Unmarshal(raw, &git); err != nil {
		t.Fatal(err)
	}
	if len(git.extra) == 0 {
		t.Fatal("a saved description silently discarded a future cleanup resource role")
	}
	encoded, err := json.Marshal(git)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil || string(fields["future_resource"]) != `{"preserve":true}` {
		t.Fatal("future worktree metadata did not survive a saved round trip", err)
	}
}
