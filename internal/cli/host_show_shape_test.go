package cli

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
)

// Unset data is omitted. Explicit empty data, zero numbers, raw host text
// and integers beyond float64's exact range retain their original meaning.
func TestHostShowInfoKeepsExactDataWithoutNull(t *testing.T) {
	for _, unset := range []bool{true, false} {
		t.Run(strconv.FormatBool(unset), func(t *testing.T) {
			d := clienttest.HostDetails()
			// A date-shaped string is still host data, not an unset time.
			d.Info.Hostname = "0001-01-01T00:00:00Z"
			d.Info.MemTotal, d.Info.CPUs = 1<<53+1, 0
			d.Info.Distro = " \r\nraw\x1bc\u202e\u200b " + strings.Repeat("x", 300)
			if unset {
				d.Info.Agents, d.Info.AgentVersions, d.Info.Checks = nil, nil, nil
			} else {
				d.Info.Agents, d.Info.Checks = map[string]string{}, []client.Check{}
			}
			before := *d.Info
			before.Agents = maps.Clone(d.Info.Agents)
			before.AgentVersions = maps.Clone(d.Info.AgentVersions)
			before.Checks = slices.Clone(d.Info.Checks)
			b, err := versioned(hostShowObject(d, nil))
			if err != nil {
				t.Fatal(err)
			}
			b = jsonSafe(b)
			checkJSON(t, "host show exact shape", string(b), false)
			if strings.ContainsAny(string(b), "\x1b\u202e\u200b") {
				t.Fatal("raw host text reached the terminal without JSON escaping")
			}
			var got struct{ Info client.HostInfo }
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Info, before) || !reflect.DeepEqual(*d.Info, before) {
				t.Fatalf("host JSON changed its data: %+v, want %+v", got.Info, before)
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal(b, &object); err != nil {
				t.Fatal(err)
			}
			var info map[string]json.RawMessage
			if err := json.Unmarshal(object["info"], &info); err != nil {
				t.Fatal(err)
			}
			_, checksPresent := info["checks"]
			_, agentsPresent := info["agents"]
			if checksPresent == unset || agentsPresent == unset {
				t.Fatalf("nil and empty fields were conflated: %s", object["info"])
			}
			if !unset && (string(info["checks"]) != "[]" || string(info["agents"]) != "{}") {
				t.Fatalf("explicit empty data was omitted or changed: %s", object["info"])
			}
			if string(info["mem_total"]) != "9007199254740993" || string(info["cpus"]) != "0" {
				t.Fatalf("integer precision or zero data changed: %s", object["info"])
			}
		})
	}
}
