//go:build linux

package host

import (
	"strings"
	"testing"
)

func TestCustodyUnitLiteralWordsCloseDirectiveInjection(t *testing.T) {
	for _, value := range []string{"/a helper/with space", "x%u$HOME\"\\\n[Service]\nExecStart=/untrusted", "\t\r\x7f"} {
		word, err := custodyUnitWordV1(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.ContainsAny(word, "\n\r\t") || strings.Contains(word, "%u") && !strings.Contains(word, "%%u") {
			t.Fatal("input escaped a closed literal unit word", word)
		}
		if !strings.HasPrefix(word, `"`) || !strings.HasSuffix(word, `"`) {
			t.Fatal("native word is not quoted", word)
		}
	}
	if _, err := custodyUnitWordV1("before\x00after"); err == nil {
		t.Fatal("NUL was accepted by native exec config")
	}
}

func TestCustodyAnchorNeverPullsInSessionUnits(t *testing.T) {
	_, pin, err := custodyPinFileV1("abc234")
	if err != nil {
		t.Fatal(err)
	}
	combined := string(custodyTargetFileV1()) + string(pin)
	for _, directive := range []string{"Wants=", "Requires=", "BindsTo=", "Upholds=", "ExecStart="} {
		if strings.Contains(combined, directive) {
			t.Fatal("passive anchor can start a session", directive)
		}
	}
	if !strings.Contains(string(pin), "After="+UnitName("abc234")+"\n") {
		t.Fatal("anchor lost its passive native reference")
	}
}
