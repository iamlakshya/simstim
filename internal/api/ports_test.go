package api

import "testing"

func TestPortTargetsHaveNoAliasesOrOutOfRangeValues(t *testing.T) {
	for _, target := range []string{"", "0", "65536", "+80", "080", " 80", "80 ", "80:81", "127.0.0.1:80", "-1", "８０"} {
		if _, err := ParsePortTarget(target); err == nil {
			t.Fatalf("ambiguous port target passed: %q", target)
		}
	}
	for _, target := range []string{"1", "80", "65535"} {
		if _, err := ParsePortTarget(target); err != nil {
			t.Fatalf("valid port target refused: %q: %v", target, err)
		}
	}
}
