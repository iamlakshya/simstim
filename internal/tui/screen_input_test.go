package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestScreenInputErrorsNameTheScreen(t *testing.T) {
	for _, what := range []string{"the launcher", "the switcher", "the demo"} {
		for _, tc := range []struct {
			name     string
			inputOff bool
			terminal bool
		}{
			{"no-terminal", false, false},
			{"no-input", true, true},
			{"no-input-and-no-terminal", true, false},
		} {
			t.Run(what+"/"+tc.name, func(t *testing.T) {
				withFakeLauncherIO(t, func(*os.File) bool { return tc.terminal }, func(context.Context, tea.Model, io.Reader, io.Writer) error {
					t.Fatal("opened a program with input unavailable")
					return nil
				})
				ctx := context.Background()
				want := what + " needs a terminal"
				code, kind := "launcher_no_terminal", client.KindFailed
				if tc.inputOff {
					ctx, want = client.WithoutInput(ctx), what+" cannot open with input off"
					code, kind = "no_input", client.KindNeedsAnswer
				}
				var err error
				switch what {
				case "the demo":
					err = RunDemo(ctx, true)
				default:
					err = runWith(ctx, newFakeBackend(), Options{Switcher: what == "the switcher"})
				}
				var ue *client.UserError
				hint := map[string]string{"the launcher": "ls", "the switcher": "flip <host>/<session>", "the demo": "help demo"}[what]
				if !errors.As(err, &ue) || ue.Code != code || ue.Kind != kind || ue.Msg != want || !strings.Contains(ue.Hint, hint) {
					t.Fatalf("input error=%v (ue=%+v), want code=%s kind=%v and %q with hint", err, ue, code, kind, want)
				}
			})
		}
	}
}
