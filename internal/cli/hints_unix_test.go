//go:build unix

package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// One code has one kind, also in the errors that the CLI makes as it runs:
// each has the kind of the literals with its code (TestOneCodeOneKind
// checks the literals against each other and the host's codes).
func TestRunTimeErrorsKeepOneKind(t *testing.T) {
	lits := literalKinds(t)
	names := map[client.Kind]string{client.KindFailed: "KindFailed", client.KindUsage: "KindUsage", client.KindNotFound: "KindNotFound",
		client.KindNeedsAnswer: "KindNeedsAnswer", client.KindRefused: "KindRefused", client.KindCancelled: "KindCancelled"}
	hosttest.New(t)
	var errs []error
	for _, args := range [][]string{{"atach"}, {"host", "ad"}, {"help", "atach"}, {"new", "--detatch"}, {"new", "--dir"},
		{"debug", "start", "a/b", "--for", "soon"}, {"attach"}, {"ls", "box"}, {"---x"}, {"attach", "nowhere/s"}} {
		_, err := line(t, args...)
		errs = append(errs, err)
	}
	errs = append(errs, errInterrupted)
	path := filepath.Join(client.ConfigDir(), "hosts.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[[host]]\nname = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := client.LoadHosts()
	errs = append(errs, err)
	for _, err := range errs {
		var ue *client.UserError
		if !errors.As(err, &ue) {
			t.Errorf("%v is not a user error", err)
			continue
		}
		if want, ok := lits[ue.Code]; !ok || names[ue.Kind] != want {
			t.Errorf("%q has the code %s and %s, and the literals give that code %q", ue.Msg, ue.Code, names[ue.Kind], want)
		}
	}
	// The cancel of the prompts, as the answer no makes it, is not a user
	// error: its report has the code and the kind of a cancel.
	for _, err := range []error{cancelled(), cancelled("saved the host box")} {
		r := describe(err, nil)
		if want, ok := lits[r.code]; !ok || names[r.kind] != want {
			t.Errorf("%q has the code %s and %s, and the literals give that code %q", r.msg, r.code, names[r.kind], want)
		}
	}
}
