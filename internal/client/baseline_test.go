package client

import (
	"strings"
	"testing"
)

func baselineWireFixtureV1() string {
	var out strings.Builder
	out.WriteString("baseline-v1\n")
	for _, key := range baselineKeysV1 {
		out.WriteString(key + "\tabsent\t\n")
	}
	return out.String()
}

func TestBaselineRejectsIncompleteDuplicateAndUnknownReadings(t *testing.T) {
	valid := baselineWireFixtureV1()
	if _, err := parseBaselineV1([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{strings.TrimSuffix(valid, "crontab\tabsent\t\n"), valid + "crontab\tabsent\t\n", strings.Replace(valid, "profile\tabsent\t", "bashrc\tabsent\t", 1), strings.Replace(valid, "bashrc\tabsent\t", "bashrc\tknown\tbad", 1)} {
		if _, err := parseBaselineV1([]byte(raw)); err == nil {
			t.Fatal("invalid host output became a complete baseline", raw)
		}
	}
}

func TestBaselineUnknownIsNeverReportedUnchanged(t *testing.T) {
	before, err := parseBaselineV1([]byte(baselineWireFixtureV1()))
	if err != nil {
		t.Fatal(err)
	}
	after := append([]baselineReadingV1(nil), before...)
	after[0] = baselineReadingV1{Key: "bashrc", State: "known", SHA256: strings.Repeat("a", 64)}
	after[8] = baselineReadingV1{Key: "crontab", State: "unknown"}
	changes := compareBaselineV1(before, after)
	if len(changes.Changed) != 1 || changes.Changed[0] != "~/.bashrc" || len(changes.Unavailable) != 1 || changes.Unavailable[0] != "crontab" {
		t.Fatal("baseline lost change or uncertainty", changes)
	}
}
