package api

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestHostSelectorNativeCodecAndOwnedRoundTrip(t *testing.T) {
	for _, text := range []string{`host = "auto"`, `host = "first"`, `host = ["second", "first"]`} {
		var source struct {
			Host *HostSelectorV1 `toml:"host" json:"host"`
		}
		if _, err := toml.Decode(text, &source); err != nil || source.Host == nil || source.Host.Validate() != nil {
			t.Fatalf("decode %s: %v", text, err)
		}
		var encoded bytes.Buffer
		if err := toml.NewEncoder(&encoded).Encode(source); err != nil {
			t.Fatal(err)
		}
		var restored struct {
			Host *HostSelectorV1 `toml:"host" json:"host"`
		}
		if _, err := toml.Decode(encoded.String(), &restored); err != nil || !reflect.DeepEqual(source, restored) {
			t.Fatalf("scalar round trip changed the choice: %q: %v", encoded.String(), err)
		}
		raw, err := json.Marshal(source)
		if err != nil || !ExactJSONFields(raw, source) || json.Unmarshal(raw, &restored) != nil || !reflect.DeepEqual(source, restored) {
			t.Fatal("the exact JSON codec changed the original scalar")
		}
	}
	input := []string{"second", "first"}
	choice, err := DecodeHostSelectorV1(input)
	input[0] = "changed"
	if err != nil || choice.Names[0] != "second" {
		t.Fatal("the scalar borrowed the caller's mutable ordered list")
	}
}

func TestHostSelectorRejectsInvalidChoicesWithoutPartialMutation(t *testing.T) {
	for _, raw := range []string{`[]`, `["auto"]`, `["one","one"]`, `[""]`, `["ends."]`, `["-bad"]`, `[1]`, `{"Names":["one"]}`, `null`} {
		original := HostSelectorV1{Names: []string{"retained"}}
		if err := json.Unmarshal([]byte(raw), &original); err == nil || !reflect.DeepEqual(original.Names, []string{"retained"}) || original.Auto {
			t.Fatalf("invalid choice changed the retained selection: %s", raw)
		}
	}
}
