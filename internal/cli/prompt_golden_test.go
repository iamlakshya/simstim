package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/helpers"
)

// Each prompt's text, against a golden file (cli-spec 6.4).
func TestGoldenPrompts(t *testing.T) {
	_, tr := withFake(t, nil)
	if _, ok := helpers.For("linux", "arm64"); !ok {
		t.Skip("this build has no helper for linux/arm64 (run just helpers)")
	}
	var b strings.Builder
	show := func(name, in string, ask func(p *prompter) error) {
		var out bytes.Buffer
		err := ask(testPrompts(in, &out))
		fmt.Fprintf(&b, "== %s (answers %q)\n%s\n", name, in, out.String())
		if err != nil {
			fmt.Fprintf(&b, "-> %v\n", err)
		}
	}
	show("new: a missing folder", "\n", func(p *prompter) error {
		_, err := askMakeFolder(p, "box", client.FolderQuestion{Folder: "/home/dev/src/new-app"})
		return err
	})
	show("new: a setup script", "y\n", func(p *prompter) error {
		_, err := askSetup(p, "box", client.SetupQuestion{Repo: "/home/dev/src/app", Script: "/home/dev/src/app/.<name>/setup"}, client.SetupNotTrusted)
		return err
	})
	show("doctor --fix", "y\n", func(p *prompter) error {
		return offerFixes(p, qFix, sampleFixes[:1], false, func([]string) ([]client.Fix, error) { return nil, nil })
	})
	_ = saveHost(context.Background(), client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}})
	show("host destroy", "no\n", func(p *prompter) error { return destroyHost(context.Background(), p, "box", false, false) })
	tr.NoHelper = true
	show("host add: the helper", "n\n", func(p *prompter) error {
		var err error
		// The helper's description goes to stderr, apart from the question.
		captureOutput(t, func() {
			err = addHost(context.Background(), p, client.Host{Name: "new", Machine: &client.Machine{Provider: "fake", ID: "fake-new"}}, false)
		})
		return err
	})
	show("three answers that are not one", "x\nmaybe\n?\n", func(p *prompter) error {
		_, err := p.yesNo("Install it?", false)
		return err
	})
	show("a typed name", "box\n", func(p *prompter) error {
		_, err := p.typedName("Type box to delete the machine, or press Enter to keep it:", "box")
		return err
	})
	running := client.Session{ID: "abcd2345", Name: "auth-fix", Agent: "claude", Cwd: "/home/dev/src/app", State: "working"}
	show("rm", "\n", func(p *prompter) error {
		ok, err := askDelete(p, "box", running)
		if err == nil && ok {
			return errors.New("Enter deleted")
		}
		return err
	})
	ended := client.Session{ID: "abcd2345", Name: "auth-fix", Agent: "claude", Cwd: "/home/dev/src/app", State: "exited", ExitCode: new(0)}
	show("rm: an ended agent", "y\n", func(p *prompter) error {
		_, err := askDelete(p, "box", ended)
		return err
	})
	show("attach: an ended agent", "\n", func(p *prompter) error {
		ok, err := askRestart(p, "box", ended)
		if err == nil && !ok {
			return errors.New("Enter did not start it again")
		}
		return err
	})
	got := strings.ReplaceAll(b.String(), brand.Name, "<name>")
	golden := filepath.Join("testdata", "prompts.golden")
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("the prompts differ from %s (run go test ./internal/cli -run TestGoldenPrompts -update, and read the diff):\n%s", golden, got)
	}
}
