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
		for _, inputOff := range []bool{false, true} {
			t.Run(what+map[bool]string{false: "/no-terminal", true: "/no-input"}[inputOff], func(t *testing.T) {
				withFakeLauncherIO(t, func(*os.File) bool { return inputOff }, func(context.Context, tea.Model, io.Reader, io.Writer) error {
					t.Fatal("opened a program with input unavailable")
					return nil
				})
				ctx := context.Background()
				want := what + " needs a terminal"
				if inputOff {
					ctx, want = client.WithoutInput(ctx), what+" cannot open with input off"
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
				if !errors.As(err, &ue) || ue.Msg != want || !strings.Contains(ue.Hint, hint) {
					t.Fatalf("input error=%v (ue=%+v), want %q with hint", err, ue, want)
				}
			})
		}
	}
}
