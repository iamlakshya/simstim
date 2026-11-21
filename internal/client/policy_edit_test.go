package client

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestPolicyEditorLocatesQuotedTablesAndExactHostArray(t *testing.T) {
	raw := "# header\n[profiles.'review.work'.network]\nunknown='deny'\n[[host]]\nname='first'\n[[host]]\nname='second'\n[repos.'forge.example/team/app'.sandbox]\nwall='process'\n"
	for _, test := range []struct {
		layer FileLayer
		line  int
	}{{FileLayer{Kind: api.LayerAll}, 1}, {FileLayer{Kind: api.LayerProfile, Name: "review.work"}, 2}, {FileLayer{Kind: api.LayerHost, Name: "second"}, 6}, {FileLayer{Kind: api.LayerRepo, Name: "forge.example/team/app"}, 8}} {
		if got := policyEditLine(raw, test.layer); got != test.line {
			t.Fatalf("selected %+v opened line %d, want %d", test.layer, got, test.line)
		}
	}
}
