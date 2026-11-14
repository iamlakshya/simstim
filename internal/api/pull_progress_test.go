package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestOutputProgressStrictFieldsPreserveExplicitZeroIDs(t *testing.T) {
	good := PullOutputProgress{Event: PullOutputProgressEvent, ID: "fixture2", Token: strings.Repeat("a", 26), Epoch: 0, Request: 0, Next: 0}
	encoded, err := EncodePullOutputProgress(good)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePullOutputProgress(encoded)
	if err != nil || !reflect.DeepEqual(good, decoded) {
		t.Fatal("explicit zero identity changed", decoded, err)
	}
	for _, missing := range []string{"event", "id", "token", "epoch", "request", "next"} {
		var fields map[string]json.RawMessage
		if err = json.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		delete(fields, missing)
		fields["future"] = json.RawMessage(`0`)
		bad, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = DecodePullOutputProgress(bad); err == nil {
			t.Fatal("missing field was replaced by unknown metadata", missing)
		}
	}
	for _, field := range []string{"epoch", "request", "next"} {
		var fields map[string]json.RawMessage
		if err = json.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		fields[field] = json.RawMessage(`null`)
		bad, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = DecodePullOutputProgress(bad); err == nil {
			t.Fatal("null was converted into a valid explicit zero", field)
		}
	}
	for _, bad := range []string{
		strings.Replace(string(encoded), `"epoch":0`, `"Epoch":0`, 1),
		strings.Replace(string(encoded), `"epoch":0`, `"epoch":0,"epoch":0`, 1),
		strings.Replace(string(encoded), `"request":0`, `"request":18446744073709551616`, 1),
		strings.Replace(string(encoded), `"next":0`, `"next":-1`, 1),
	} {
		if _, err = DecodePullOutputProgress([]byte(bad)); err == nil {
			t.Fatal("ambiguous or invalid progress accepted", bad)
		}
	}
}
