package cli

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func TestNamedSSHKeyDetailsSeparateInstalledAndNextStart(t *testing.T) {
	session := client.Session{Alive: true, NamedSSHKeys: &api.NamedSSHKeyViewV1{V: 1, Installed: []string{"DEPLOY_KEY"}, NextStart: []string{"OTHER_KEY"}}}
	if showNamedSSHKeys(session) != "DEPLOY_KEY" {
		t.Fatal("installed key display is not actual owner data")
	}
	if next := showNextNamedSSHKeys(session); !strings.Contains(next, "OTHER_KEY") || !strings.Contains(next, "next start") {
		t.Fatal("pending selection was shown as active")
	}
	session.NamedSSHKeys.NextStart = nil
	if showNextNamedSSHKeys(session) != "none (at the next start)" {
		t.Fatal("pending removal disappeared")
	}
	session.Alive = false
	if showNamedSSHKeys(session) != "" || showNextNamedSSHKeys(session) != "" {
		t.Fatal("stopped session claimed a live key owner")
	}
}
