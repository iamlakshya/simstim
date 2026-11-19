package egress

import (
	"strings"
	"testing"
)

func TestResponseFiniteASCIIWidthsKeepLiteralClassAndWindowBounds(t *testing.T) {
	for _, tc := range []struct {
		name, expression, value string
		window                  int
	}{
		{"default", `((?:abcdef[A-Z]){24})`, strings.Repeat("abcdefZ", 24), responsePatternWindow},
		{"service", `(token_[A-Z]{1000})`, "token_" + strings.Repeat("Z", 1000), responseServicePatternWindow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules, err := newResponseRuleSnapshotWithinWindow([]responseMintRule{{ID: tc.name, Expression: tc.expression}}, tc.window)
			if err != nil {
				t.Fatal("finite ASCII literal and class grammar was refused", err)
			}
			match := rules.match([]byte(tc.value), true, 0, false)
			if match.wait || match.ruleID != tc.name || match.length != len(tc.value) {
				t.Fatal("finite width truncated the complete source capture")
			}
		})
	}
	for _, expression := range []string{
		`((?:abcdef[A-Z]){25})`, // 175 bytes exceeds the original finite window.
		`(token_[A-Z]{1000})`,
		`(ascii[\x{80}-\x{ff}])`,
	} {
		if _, err := newResponseRuleSnapshot([]responseMintRule{{ID: "invalid", Expression: expression}}); err == nil {
			t.Fatal("custom grammar escaped the unchanged ASCII/170-byte bound")
		}
	}
}
