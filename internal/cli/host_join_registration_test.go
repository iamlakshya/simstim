package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestJoinedAcceptanceOutputRequiresActualTestedSummary(t *testing.T) {
	for _, test := range []struct {
		name                           string
		skipped, tested, noWall, ready bool
	}{
		{"tested", false, true, false, true}, {"skipped", true, false, false, false}, {"no-wall", false, false, true, false}, {"unconfirmed", false, false, false, false}, {"skip-is-not-boot", true, true, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			summary := hostPrepareSummary{outcome: prepareOutcomeView{tested: test.tested, noRunnableWall: test.noWall}}
			printJoinedHostOutcomeV1(&output, "box", test.skipped, summary)
			if got := strings.Contains(output.String(), "is ready"); got != test.ready {
				t.Fatalf("readiness output: %s", output.String())
			}
			if !test.ready && (!strings.Contains(output.String(), "was saved") || !strings.Contains(output.String(), "host prepare box")) {
				t.Fatalf("unconfirmed save lost its honest follow-up: %s", output.String())
			}
		})
	}
}
