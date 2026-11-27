package client

import (
	"reflect"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestSecretPresentationUsesOnlyCompleteSelectedSummary(t *testing.T) {
	names := []string{"VISIBLE", "BOUND"}
	complete := &api.SessionSecretSummary{Version: api.SessionSecretSummaryV1, Generation: "selected-generation", Secrets: map[string]api.SecretStatus{"BOUND": {Mode: api.SecretModeBound, Armed: false}, "VISIBLE": {Mode: api.SecretModeExposed, Armed: true}}}
	session := Session{Secrets: names, SecretSummary: complete, Armed: true}
	want := SecretPresentation{Exposed: []string{"VISIBLE"}, Unarmed: []string{"BOUND"}}
	if got := PresentSessionSecrets(session); !reflect.DeepEqual(got, want) {
		t.Fatalf("delivery modes/statuses were inferred incorrectly: %+v", got)
	}
	for _, row := range []struct {
		name    string
		summary *api.SessionSecretSummary
	}{
		{"absent", nil},
		{"future", &api.SessionSecretSummary{Version: api.SessionSecretSummaryV1 + 1}},
		{"unsupported", &api.SessionSecretSummary{Version: api.SessionSecretSummaryV1, Unsupported: true}},
		{"no generation", &api.SessionSecretSummary{Version: api.SessionSecretSummaryV1, Secrets: complete.Secrets}},
		{"missing member", &api.SessionSecretSummary{Version: api.SessionSecretSummaryV1, Generation: "selected-generation", Secrets: map[string]api.SecretStatus{"BOUND": {Mode: api.SecretModeBound, Armed: true}}}},
		{"extra member", &api.SessionSecretSummary{Version: api.SessionSecretSummaryV1, Generation: "selected-generation", Secrets: map[string]api.SecretStatus{"BOUND": {Mode: api.SecretModeBound, Armed: true}, "VISIBLE": {Mode: api.SecretModeExposed, Armed: true}, "OTHER": {Mode: api.SecretModeExposed, Armed: true}}}},
	} {
		t.Run(row.name, func(t *testing.T) {
			session.SecretSummary = row.summary
			got := PresentSessionSecrets(session)
			if len(got.Exposed) != 0 || len(got.Unarmed) != 0 || !reflect.DeepEqual(got.Unknown, []string{"BOUND", "VISIBLE"}) {
				t.Fatalf("unknown/partial data became delivery authority: %+v", got)
			}
		})
	}
	if !reflect.DeepEqual(names, []string{"VISIBLE", "BOUND"}) || complete.Secrets["BOUND"].Armed {
		t.Fatal("presentation changed the raw session/summary")
	}
	result := PresentSessionSecrets(Session{Secrets: names})
	result.Unknown[0] = "changed"
	if names[0] != "VISIBLE" {
		t.Fatal("presentation returned a borrowed names slice")
	}
}
