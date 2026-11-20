package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestEmptyEditorCommandReportsErrorWithoutTakingTerminal(t *testing.T) {
	for _, variable := range []string{"EDITOR", "VISUAL"} {
		t.Run(variable, func(t *testing.T) {
			t.Setenv("EDITOR", "vi")
			t.Setenv("VISUAL", "")
			t.Setenv(variable, " \t ")
			hs := newHarness(t, newFakeBackend(), 90, 30)
			executed := 0
			hs.m.exec = func(tea.ExecCommand, tea.ExecCallback) tea.Cmd {
				executed++
				return nil
			}
			func() {
				defer func() {
					if panicValue := recover(); panicValue != nil {
						t.Fatalf("empty editor command crashed: %v", panicValue)
					}
				}()
				hs.do(hs.m.openSettings())
			}()
			if executed != 0 || hs.quit || len(hs.m.toasts.shown) == 0 {
				t.Fatalf("empty editor handed off/quit or omitted error: exec=%d quit=%v toasts=%+v", executed, hs.quit, hs.m.toasts.shown)
			}
			note := hs.m.toasts.shown[len(hs.m.toasts.shown)-1]
			if note.kind != toastError || !strings.Contains(note.text, "editor command is empty") || !strings.Contains(note.hint, "$EDITOR") || !strings.Contains(note.hint, "$VISUAL") {
				t.Fatalf("empty editor notice = %+v", note)
			}
			hs.send(tea.KeyPressMsg{Code: 'q', Text: "q"})
			if !hs.quit {
				t.Fatal("launcher no longer handles keys after an empty editor command")
			}
		})
	}
}
