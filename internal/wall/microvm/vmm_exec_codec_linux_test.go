//go:build linux

package microvm

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestFinalModeV2RequiresOneContinuationAndPreservesSealedEOF(t *testing.T) {
	legacy := runnerPlan{Version: 1}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	plan, continuation, err := readFinalExecPlanV1(bytes.NewReader(data))
	if err != nil || plan.Version != 1 || continuation != nil {
		t.Fatal("sealed EOF codec changed", err)
	}
	if _, _, err := readFinalExecPlanV1(bytes.NewReader(append(data, data...))); err == nil {
		t.Fatal("sealed trailing object was accepted")
	}
	for _, extra := range [][]byte{nil, {0}, {finalExecContinueV2, 1}, {finalExecContinueV2}} {
		var wire bytes.Buffer
		if err := writeFinalExecPlanV2(&wire, runnerPlan{Version: 2}); err != nil {
			t.Fatal(err)
		}
		wire.Write(extra)
		plan, reader, err := readFinalExecPlanV1(&wire)
		if err != nil || plan.Version != 2 {
			t.Fatal(err)
		}
		err = waitFinalExecContinueV2(reader)
		good := len(extra) == 1 && extra[0] == finalExecContinueV2
		if (err == nil) != good {
			t.Fatal("unknown, early EOF or surplus continuation changed authority", extra, err)
		}
	}
	if _, _, err := readFinalExecPlanV1(strings.NewReader(finalExecPlanPrefixV2 + `{"version":2,"unknown":true}` + "\n")); err == nil {
		t.Fatal("unknown v2 plan field was accepted")
	}
}
