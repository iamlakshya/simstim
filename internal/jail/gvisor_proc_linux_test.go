package jail

import (
	"strings"
	"testing"
)

func TestGvisorNativeTaskProofRejectsMalformedEndedAndReplacedBirth(t *testing.T) {
	// Linux fields 3..22 are state, PPid and 18 further fields, ending in birth.
	text := "2 (trusted helper (name)) S 1 " + strings.Repeat("0 ", 17) + "1234\n"
	got, err := parseGvisorTaskV2([]byte(text))
	if err != nil || got.pid != 2 || got.parent != 1 || got.birth != 1234 {
		t.Fatalf("native task parse: %+v %v", got, err)
	}
	for _, bad := range []string{"", "0 (helper) S 1", strings.Replace(text, " S ", " Z ", 1), strings.Replace(text, "1234", "+1234", 1), strings.Replace(text, " S 1 ", " S -1 ", 1), strings.Replace(text, "2 (", "02 (", 1)} {
		if _, err := parseGvisorTaskV2([]byte(bad)); err == nil {
			t.Fatalf("malformed native proof passed: %q", bad)
		}
	}
	changed, err := parseGvisorTaskV2([]byte(strings.Replace(text, "1234", "1235", 1)))
	if err != nil || changed == got {
		t.Fatal("a different native birth could stand for the original task")
	}
}

func TestGvisorBootstrapCannotReorderOrDuplicateFixedDescriptorRoles(t *testing.T) {
	if !equalGvisorSlotsV2([]int{0, 1, 2, 3, 7}, []int{0, 1, 2, 3, 7}) {
		t.Fatal("the original exact vector was rejected")
	}
	for _, other := range [][]int{{0, 1, 2}, {0, 1, 2, 3, 3}, {0, 1, 2, 7, 3}, {0, 1, 2, 3, 8}} {
		if equalGvisorSlotsV2([]int{0, 1, 2, 3, 7}, other) {
			t.Fatal("a substituted native role vector passed", other)
		}
	}
}
