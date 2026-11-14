package client

import (
	"errors"
	"strings"
	"testing"
)

func TestAlertCredentialBoundary(t *testing.T) {
	for _, test := range []struct {
		name  string
		dest  AlertDestination
		token string
	}{
		{"token command", AlertDestination{Type: "ntfy", URL: "https://notify.example/topic"}, "read-token"},
		{"inline token", AlertDestination{Type: "ntfy", URL: "https://notify.example/topic", Token: "hidden"}, ""},
		{"token file", AlertDestination{Type: "ntfy", URL: "https://notify.example/topic", TokenFile: "token"}, ""},
		{"header", AlertDestination{Type: "webhook", URL: "https://notify.example/events", Headers: map[string]string{"X-Team": "hidden"}}, ""},
		{"userinfo", AlertDestination{Type: "webhook", URL: "https://user:hidden@notify.example/events"}, ""},
		{"encoded query key", AlertDestination{Type: "webhook", URL: "https://notify.example/events?%74oken=hidden"}, ""},
		{"SAS query", AlertDestination{Type: "webhook", URL: "https://notify.example/events?sv=1&sig=hidden"}, ""},
		{"bearer query", AlertDestination{Type: "webhook", URL: "https://notify.example/events?BEARER=hidden"}, ""},
		{"code query", AlertDestination{Type: "webhook", URL: "https://notify.example/events?code=hidden"}, ""},
		{"custom slack", AlertDestination{Type: "webhook", URL: "https://notify.example/events", Format: "slack"}, ""},
		{"custom discord", AlertDestination{Type: "webhook", URL: "https://notify.example/events", Format: "discord"}, ""},
		{"Slack", AlertDestination{Type: "webhook", URL: "https://hooks.slack.com/services/hidden"}, ""},
		{"Discord", AlertDestination{Type: "webhook", URL: "https://discord.com/api/webhooks/hidden"}, ""},
		{"Office", AlertDestination{Type: "webhook", URL: "https://team.webhook.office.com/hidden"}, ""},
		{"Azure", AlertDestination{Type: "webhook", URL: "https://team.logic.azure.com/hidden"}, ""},
		{"Zapier", AlertDestination{Type: "webhook", URL: "https://hooks.zapier.com/hidden"}, ""},
		{"IFTTT", AlertDestination{Type: "webhook", URL: "https://maker.ifttt.com/hidden"}, ""},
		{"Telegram", AlertDestination{Type: "webhook", URL: "https://api.telegram.org/bothidden/sendMessage"}, ""},
		{"Google Chat", AlertDestination{Type: "webhook", URL: "https://chat.googleapis.com/hidden"}, ""},
		{"PagerDuty", AlertDestination{Type: "webhook", URL: "https://events.pagerduty.com/hidden"}, ""},
		{"generic hooks", AlertDestination{Type: "webhook", URL: "https://notify.example/hooks/hidden"}, ""},
		{"generic webhook", AlertDestination{Type: "webhook", URL: "https://notify.example/api/webhook/hidden"}, ""},
		{"generic webhooks", AlertDestination{Type: "webhook", URL: "https://notify.example/api/webhooks/hidden"}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			label, credential, err := AlertCredentialKind(test.dest, test.token)
			if err != nil || !credential || !ValidAlertGrantLabel(label) || strings.Contains(label, "hidden") {
				t.Fatalf("label=%q credential=%v err=%v", label, credential, err)
			}
			for _, tier := range []Tier{TierThrowaway, TierShared} {
				cfg := AlertsConfig{Destinations: []AlertDestination{test.dest}}
				err := checkAlertGrants(Host{Name: "box"}, tier, cfg, test.token != "", Settings{})
				var refused *UserError
				if !errors.As(err, &refused) || refused.Kind != KindRefused || refused.Code != "tier_refused" ||
					strings.Contains(err.Error(), "hidden") {
					t.Fatalf("%s: credential was allowed or exposed: %v", tier, err)
				}
			}
		})
	}
}

func TestPlainAlertsStayAvailableBelowOwn(t *testing.T) {
	for _, d := range []AlertDestination{
		{Type: "command", Run: "notify-event"},
		{Type: "ntfy", URL: "topic"},
		{Type: "ntfy", URL: "https://notify.example/topic"},
		{Type: "webhook", URL: "https://notify.example/events?channel=build&monkey=one", Format: "json"},
	} {
		cfg := AlertsConfig{Destinations: []AlertDestination{d}}
		for _, tier := range []Tier{TierOwn, TierThrowaway, TierShared} {
			if err := checkAlertGrants(Host{Name: "box"}, tier, cfg, false, Settings{}); err != nil {
				t.Errorf("%s/%s: %v", tier, d.Type, err)
			}
		}
	}
}

func TestUnreadableAlertAddressFailsClosedWithoutShowingIt(t *testing.T) {
	for _, raw := range []string{"https://notify.example/%zz-hidden", "https://notify.example/events?token=%zz-hidden", "file:///hidden", "https://"} {
		cfg := AlertsConfig{Destinations: []AlertDestination{{Type: "webhook", URL: raw}}}
		for _, tier := range []Tier{TierOwn, TierThrowaway, TierShared} {
			err := checkAlertGrants(Host{Name: "box"}, tier, cfg, false, Settings{})
			var ue *UserError
			if !errors.As(err, &ue) || ue.Hint == "" || strings.Contains(err.Error(), "hidden") {
				t.Fatalf("%s: unreadable URL was allowed or exposed: %v", tier, err)
			}
			if tier < TierOwn && ue.Kind != KindRefused {
				t.Fatalf("%s: unreadable URL did not fail as a policy refusal: %v", tier, err)
			}
		}
	}
}

func TestAlertLedgerLabelsAreDeduplicatedAndContainNoCredential(t *testing.T) {
	cfg := AlertsConfig{Destinations: []AlertDestination{
		{Type: "ntfy", URL: "https://notify.example/hidden", Token: "hidden"},
		{Type: "ntfy", URL: "https://notify.example/other", Token: "other"},
		{Type: "webhook", URL: "https://HOOKS.EXAMPLE/events?token=hidden"},
		{Type: "webhook", URL: "https://hooks.example/hooks/other"},
	}}
	grants, err := alertTokenGrants(cfg, false)
	if err != nil || len(grants) != 2 || grants[0].Name != "ntfy" || grants[1].Name != "webhook:hooks.example" {
		t.Fatalf("grants=%+v err=%v", grants, err)
	}
	if err := checkAlertGrants(Host{Name: "box"}, TierOwn, cfg, false, Settings{}); err != nil {
		t.Fatalf("own host could not get alert credentials: %v", err)
	}
}
