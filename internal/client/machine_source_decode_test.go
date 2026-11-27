package client

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func sourceDocument(host, machine, source, version, extra string) string {
	return "[[" + host + "]]\nname='box'\n[" + host + "." + machine + "]\nprovider='docker'\nid='original'\n[" + host + "." + machine + "." + source + "]\nversion=" + version + "\nkind='docker-engine'\nresource_id='" + strings.Repeat("a", 64) + "'\nendpoint='unix:///observed/engine'\nengine_id='observed-engine'\n" + extra + "[" + host + "." + machine + "." + source + ".config]\nrole='unused'\n"
}

func TestMachineSourceUsesSameC9TableForUnknownFutureAndParentAliases(t *testing.T) {
	for _, raw := range []string{sourceDocument("host", "machine", "source", "2", "future=[1,2]\n"), sourceDocument("Host", "machine", "source", "1", ""), sourceDocument("host", "Machine", "source", "1", ""), sourceDocument("host", "machine", "Source", "1", ""), sourceDocument("host", "machine", "source", "1", "future=[1,2]\n")} {
		var parsed hostsFile
		if _, err := decodeConfig(raw, &parsed); err != nil {
			t.Fatal(err)
		}
		source := parsed.Hosts[0].Machine.Source
		if source == nil || source.opaque == nil || len(source.opaque.keys) != 3 {
			t.Fatal("source domain lost original future/alias document")
		}
		if _, err := NormaliseMachineSource("docker", source); err == nil {
			t.Fatal("opaque source became a current fact")
		}
		clone := CloneMachineSource(source)
		if !reflect.DeepEqual(source, clone) {
			t.Fatal("clone changed future source data")
		}
		clone.opaque.table.(map[string]any)["new"] = true
		if _, ok := source.opaque.table.(map[string]any)["new"]; ok {
			t.Fatal("clone aliases future source table")
		}
		var changed hostsFile
		if _, err := decodeConfig(raw+"\n# changed document\n", &changed); err != nil {
			t.Fatal(err)
		}
		if !SameEntry(parsed.Hosts[0], changed.Hosts[0]) {
			t.Fatal("an unrelated document change altered opaque source identity")
		}
		changed.Hosts[0].Machine.Source.opaque.table.(map[string]any)["future_changed"] = true
		if SameEntry(parsed.Hosts[0], changed.Hosts[0]) {
			t.Fatal("a changed source-domain value was erased")
		}
	}
	var current hostsFile
	if _, err := decodeConfig(sourceDocument("host", "machine", "source", "1", ""), &current); err != nil {
		t.Fatal(err)
	}
	if _, err := NormaliseMachineSource("docker", current.Hosts[0].Machine.Source); err != nil {
		t.Fatal(err)
	}
}

func TestMachineSourceJSONFutureAndAliasesCannotRoundTripIntoAuthority(t *testing.T) {
	raw := `{ "Version":2,"Kind":"docker-engine","ResourceID":"` + strings.Repeat("a", 64) + `","Endpoint":"unix:///observed/engine","EngineID":"observed","Config":{"Role":"unused"},"future":{"list":[1,2]} }`
	var source MachineSourceProvenance
	if err := json.Unmarshal([]byte(raw), &source); err != nil {
		t.Fatal(err)
	}
	clone := CloneMachineSource(&source)
	encoded, err := clone.MarshalJSON()
	if err != nil || string(encoded) != raw {
		t.Fatal("future JSON bytes did not stay owned")
	}
	if _, err := NormaliseMachineSource("docker", clone); err == nil {
		t.Fatal("future JSON source became current")
	}
	var alias MachineSourceProvenance
	aliased := strings.Replace(raw, `"Version":2`, `"version":1`, 1)
	if err := json.Unmarshal([]byte(aliased), &alias); err == nil {
		if _, err := NormaliseMachineSource("docker", &alias); err == nil {
			t.Fatal("JSON alias became a current source")
		}
	}
}
