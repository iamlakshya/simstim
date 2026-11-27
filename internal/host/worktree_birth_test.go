package host

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWorktreeBirthSchemaDoesNotUpgradeDataV2(t *testing.T) {
	valid := `{"v":2,"filesystem":61267,"fsid":[3,4],"type":1,"handle":"AQIDBA=="}`
	var birth worktreeBirthV2
	if err := json.Unmarshal([]byte(valid), &birth); err != nil || !birth.knownV2() {
		t.Fatalf("known native record shape: %v", err)
	}
	for _, raw := range []string{
		strings.Replace(valid, `"v":2`, `"v":2,"v":2`, 1),
		strings.Replace(valid, `"v":2`, `"V":2`, 1),
		strings.Replace(valid, `"v":2`, `"v":2,"owned":true`, 1),
		strings.Replace(valid, `[3,4]`, `[3,4,5]`, 1),
		strings.Replace(valid, `[3,4]`, `[3]`, 1),
	} {
		if err := json.Unmarshal([]byte(raw), &birth); err == nil {
			t.Fatalf("accepted unknown birth schema: %s", raw)
		}
	}
	for _, raw := range []string{
		strings.Replace(valid, `"v":2`, `"v":3`, 1),
		strings.Replace(valid, `[3,4]`, `[0,0]`, 1),
		strings.Replace(valid, `"type":1`, `"type":0`, 1),
		strings.Replace(valid, `"AQIDBA=="`, `""`, 1),
	} {
		var unknown worktreeBirthV2
		if err := json.Unmarshal([]byte(raw), &unknown); err != nil {
			t.Fatal(err)
		}
		if unknown.knownV2() {
			t.Fatalf("unknown native birth became known: %s", raw)
		}
	}
	birth.Handle = make([]byte, 129)
	if birth.knownV2() {
		t.Fatal("oversized native handle grants a known birth")
	}
	var legacy worktreeOriginV1
	if err := json.Unmarshal([]byte(`{"relative":"old"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.Birth != nil || legacy.Birth.knownV2() {
		t.Fatal("legacy metadata acquired a native generation")
	}
}
