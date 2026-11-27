//go:build darwin || linux

package client

import "testing"

func TestClosedAgentHostSelectorKeepsCanonicalUnionTypes(t *testing.T) {
	mode := newFixtureAgentSettingsMode(t)
	inspector, err := newAgentSettingsInspector(mode)
	if err != nil {
		t.Fatal(err)
	}
	for _, choice := range []string{`"auto"`, `"one"`, `["one","two"]`, `[]`, `["one",1]`, `{names=["one"]}`, `false`} {
		writeAgentSettingsFixture(t, "[profiles.work.sandbox]\nhost="+choice+"\n")
		settings, _ := ReadSettings()
		want := Allow(Host{Name: "saved"}, TierOwn, Outbound{Kind: OutboundSSHAgent}, settings)
		got := inspector.allow(t.Context(), Host{Name: "saved"}, TierOwn, Outbound{Kind: OutboundSSHAgent})
		if (want == nil) != (got == nil) {
			t.Fatalf("streaming and actual typed selector disagree for %s: want %v got %v", choice, want, got)
		}
		if mode.opened != mode.closed {
			t.Fatal("the selector type check retained its original index")
		}
	}
}
