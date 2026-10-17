package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func TestAskSetup(t *testing.T) {
	q := client.SetupQuestion{Repo: "/home/me/src/app", Script: "/home/me/src/app/.x/setup"}
	for in, want := range map[string]bool{"y\n": true, "yes\n": true, "\n": false, "n\n": false, "": false} {
		var out bytes.Buffer
		if got := askSetup(strings.NewReader(in), &out, "box", q); got != want {
			t.Errorf("%q: %v, want %v", in, got, want)
		}
		s := out.String()
		if !strings.Contains(s, "The repository /home/me/src/app on box has a setup script:\n\n    /home/me/src/app/.x/setup") ||
			!strings.Contains(s, "Say yes only for a repository you trust") || !strings.HasSuffix(s, "Run it? [y/N] ") {
			t.Fatalf("prompt:\n%s", s)
		}
	}
	if setupValue(true) != api.SetupRun || setupValue(false) != api.SetupSkip {
		t.Fatal("setupValue")
	}
}

func TestAskMakeFolder(t *testing.T) {
	q := client.FolderQuestion{Folder: "/home/me/src/new-app"}
	for in, want := range map[string]bool{"y\n": true, "yes\n": true, "\n": false, "n\n": false, "": false} {
		var out bytes.Buffer
		if got := askMakeFolder(strings.NewReader(in), &out, "box", q); got != want {
			t.Errorf("%q: %v, want %v", in, got, want)
		}
		if s := out.String(); s != "The folder /home/me/src/new-app does not exist on box.\nMake it? [y/N] " {
			t.Fatalf("prompt:\n%s", s)
		}
	}
}
