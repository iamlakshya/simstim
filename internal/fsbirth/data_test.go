package fsbirth

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFiniteGenerationDataRefusesAliasesAndUnknowns(t *testing.T) {
	raw := `{"v":2,"filesystem":61267,"fsid":[3,4],"type":1,"handle":"AQIDBA=="}`
	var original DataV2
	if err := json.Unmarshal([]byte(raw), &original); err != nil || !original.Known() {
		t.Fatalf("finite data: %v", err)
	}
	for _, changed := range []string{
		strings.Replace(raw, `"v":2`, `"v":2,"v":2`, 1),
		strings.Replace(raw, `"v":2`, `"V":2`, 1),
		strings.Replace(raw, `"v":2`, `"v":2,"source":true`, 1),
		strings.Replace(raw, `[3,4]`, `[3,4,5]`, 1),
		strings.Replace(raw, `[3,4]`, `null`, 1),
	} {
		var data DataV2
		if err := json.Unmarshal([]byte(changed), &data); err == nil {
			t.Fatalf("accepted unknown data: %s", changed)
		}
	}
	var future DataV2
	if err := json.Unmarshal([]byte(strings.Replace(raw, `"v":2`, `"v":3`, 1)), &future); err != nil {
		t.Fatal(err)
	}
	if future.Known() || Same(&original, &future) || Same(&original, nil) {
		t.Fatal("unknown data became a matching native generation")
	}
	changed := original
	changed.Handle = []byte{1, 2, 3, 5}
	if Same(&original, &changed) {
		t.Fatal("opaque handle changes were ignored")
	}
}
