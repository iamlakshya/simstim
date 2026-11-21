package client

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestNamedSSHKeyDisplayOwnsListsAndRejectsInvalidShape(t *testing.T) {
	view := &api.NamedSSHKeyViewV1{V: 1, Installed: []string{"DEPLOY_KEY"}, NextStart: []string{"OTHER_KEY"}}
	clean := CleanSession(Session{NamedSSHKeys: view})
	if clean.NamedSSHKeys == nil {
		t.Fatal("valid display refused")
	}
	clean.NamedSSHKeys.Installed[0] = "CHANGED"
	clean.NamedSSHKeys.NextStart[0] = "CHANGED"
	if view.Installed[0] != "DEPLOY_KEY" || view.NextStart[0] != "OTHER_KEY" {
		t.Fatal("display aliases source lists")
	}
	view.NextStart = []string{"OTHER_KEY\nforged"}
	if CleanSession(Session{NamedSSHKeys: view}).NamedSSHKeys != nil {
		t.Fatal("invalid display names survived")
	}
}
