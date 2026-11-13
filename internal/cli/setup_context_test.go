package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

// The save boundary owns the question's context and a signal. A cancel there
// must neither keep the new hash nor recover a broken answers file. These
// checks exercise the CLI route, rather than the compatibility client API.
func TestRememberAnswerDoesNotSaveAfterACancel(t *testing.T) {
	for _, signal := range []bool{false, true} {
		name := "context"
		if signal {
			name = "signal"
		}
		t.Run(name, func(t *testing.T) {
			withSignal(t)
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			if err := os.MkdirAll(client.StateDir(), 0o700); err != nil {
				t.Fatal(err)
			}
			trust := filepath.Join(client.StateDir(), "setup-trust.json")
			const original = "{broken"
			if err := os.WriteFile(trust, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if signal {
				close(interrupted)
			} else {
				cancel()
			}
			var out bytes.Buffer
			q := client.SetupQuestion{Repo: "/home/me/src/app", Script: "/home/me/src/app/.x/setup", Hash: "h-new"}
			err := rememberAnswer(ctx, &out, "box", q, true)
			var ue *client.UserError
			if !errors.Is(err, context.Canceled) || !errors.As(err, &ue) || ue.Kind != client.KindCancelled {
				t.Errorf("save after %s = %v, want a cancelled change", name, err)
			}
			if got, readErr := os.ReadFile(trust); readErr != nil || string(got) != original {
				t.Errorf("save after %s changed the answers: %q, %v", name, got, readErr)
			}
			if _, err := os.Stat(trust + ".bad"); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("save after %s recovered the answers file: %v", name, err)
			}
			if out.Len() != 0 {
				t.Errorf("save after %s printed a recovery warning: %s", name, out.String())
			}
		})
	}
}
