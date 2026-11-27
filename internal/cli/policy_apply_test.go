package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestPolicyApplyAndLearnRequireAnswerBeforeContactOrWrites(t *testing.T) {
	for _, args := range [][]string{{"policy", "apply", "--no-input"}, {"policy", "learn", "box/root2345", "--all", "--no-input"}} {
		t.Run(args[1], func(t *testing.T) {
			cliPolicyHome(t)
			root := Root()
			root.SetArgs(args)
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			err := root.ExecuteContext(t.Context())
			var user *client.UserError
			if !errors.As(err, &user) || user.Kind != client.KindNeedsAnswer {
				t.Fatalf("command did not require its own answer: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(client.StateDir(), "policy.lock")); !os.IsNotExist(err) {
				t.Fatal("unanswered command created a mutation lock", err)
			}
		})
	}
}

func TestPolicyApplyDryRunDoesNotCreatePendingStateOrPolicyLock(t *testing.T) {
	cliPolicyHome(t)
	root := Root()
	root.SetArgs([]string{"policy", "apply", "--dry-run", "--no-input"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"policy.lock", "policy-pending.json"} {
		if _, err := os.Lstat(filepath.Join(client.StateDir(), name)); !os.IsNotExist(err) {
			t.Fatal("dry run created state", name, err)
		}
	}
}

func TestPolicyEditRefusesSessionLayerBeforeEditor(t *testing.T) {
	cliPolicyHome(t)
	t.Setenv("EDITOR", "an-editor-that-must-not-run")
	root := Root()
	root.SetArgs([]string{"policy", "edit", "--in", "box/root2345", "--no-input"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	err := root.ExecuteContext(t.Context())
	var user *client.UserError
	if !errors.As(err, &user) || user.Code != "layer_key" {
		t.Fatal("session edit reached editor or host work", err)
	}
}
