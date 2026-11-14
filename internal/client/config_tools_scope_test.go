//go:build darwin || linux

package client

import "testing"

func TestActualOrdinaryAndClosedSettingsBothRefuseFileTools(t *testing.T) {
	mode := newFixtureAgentSettingsMode(t)
	inspector, err := newAgentSettingsInspector(mode)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		"[network]\ntools=[]\n",
		"[[network.rules]]\ntarget='api.openai.com'\nallow=true\nTools=['mcp']\n",
		"[profiles.review.network]\ntools=['files']\n",
		"[[repos.'forge.example/team/app'.network.rules]]\ntarget='api.openai.com'\nallow=true\ntools=[]\n",
	} {
		writeAgentSettingsFixture(t, body)
		settings, warning := ReadSettings()
		if warning == "" || ValidateSettings(settings) == nil {
			t.Fatal("ordinary file tools became empty/plain authority")
		}
		if err := inspector.allow(t.Context(), Host{Name: "saved"}, TierOwn, Outbound{Kind: OutboundSSHAgent}); err == nil {
			t.Fatal("closed visitor ignored file tools")
		}
		if mode.opened != mode.closed {
			t.Fatal("tools refusal retained metadata resources")
		}
	}
}
