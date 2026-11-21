package macfacts

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPowerFactsKeepUnknownSeparateFromAC(t *testing.T) {
	battery, known, percent, percentKnown := ParseBattery("Now drawing from 'Battery Power'\n -InternalBattery-0 72%; discharging; 3:12 remaining\n")
	if !battery || !known || percent != 72 || !percentKnown {
		t.Fatal("battery sample was lost")
	}
	battery, known, _, _ = ParseBattery("Now drawing from 'AC Power'\n -InternalBattery-0 100%; charged\n")
	if battery || !known {
		t.Fatal("AC sample was not observed")
	}
	if _, known, _, _ := ParseBattery("permission denied"); known {
		t.Fatal("missing evidence became AC power")
	}
	if value, known := ParseACSleep("Battery Power:\n sleep 5\nAC Power:\n sleep 0\n"); !known || value != 0 {
		t.Fatal("battery value became AC undo")
	}
	if _, known := ParseACSleep("Battery Power:\n sleep 0\n"); known {
		t.Fatal("a missing AC section became healthy")
	}
}

func TestFixedFactsRejectAliasesDuplicatesAndUnboundedText(t *testing.T) {
	facts := Facts{Version: Version, Kind: "mac", MacOS: "27.0", Chip: "Apple M3", StartMode: "daemon"}
	data, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Decode(data); err != nil || got != facts {
		t.Fatal("fixed fact round trip failed", err)
	}
	for _, bad := range []string{strings.Replace(string(data), `"v":1`, `"v":1,"v":1`, 1), strings.Replace(string(data), `"v":1`, `"V":1`, 1), strings.Replace(string(data), `"v":1`, `"v":2`, 1), strings.Replace(string(data), `"chip":"Apple M3"`, `"chip":"bad\u001b[31m"`, 1), string(data) + " {}", strings.Repeat("x", MaxBytes+1)} {
		if _, err := Decode([]byte(bad)); err == nil {
			t.Fatal("untrusted facts were accepted")
		}
	}
}

func TestAssertionsAndThermalFactsDropRawProcessDetails(t *testing.T) {
	if prevented, known := ParseSleepAssertions("PreventSystemSleep 0\nPreventUserIdleSystemSleep 1\nuser process assertion private description\n"); !known || !prevented {
		t.Fatal("fixed counters missing")
	}
	if _, known := ParseSleepAssertions("PreventSystemSleep 0\n"); known {
		t.Fatal("incomplete assertions became known")
	}
	if warning, known := ParseThermal("CPU_Speed_Limit = 100\n"); warning || !known {
		t.Fatal("full speed became a warning")
	}
	if warning, known := ParseThermal("CPU_Speed_Limit = 50\n"); !warning || !known {
		t.Fatal("native thermal limit was lost")
	}
	if _, known := ParseThermal("unknown output"); known {
		t.Fatal("unknown thermal output became healthy")
	}
	if enrolled, known := ParseMDM("Enrolled via DEP: Yes\nMDM enrollment: Yes (User Approved)\n"); !known || !enrolled {
		t.Fatal("MDM status missing")
	}
}
