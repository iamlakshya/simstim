package api

import (
	"strings"
	"testing"
)

func TestValidSessionIDKeepsTheRawLegacyForm(t *testing.T) {
	for _, id := range []string{"-", "_", "legacyID_1-", strings.Repeat("a", 64)} {
		if !ValidSessionID(id) {
			t.Errorf("valid legacy ID rejected: %q", id)
		}
	}
	for _, id := range []string{"", " ", "id name", "id;command", "id/child", "\u202eid1", "id\u200b1", "id1\n", strings.Repeat("a", 65)} {
		if ValidSessionID(id) {
			t.Errorf("bad raw ID accepted: %q", id)
		}
	}
}
