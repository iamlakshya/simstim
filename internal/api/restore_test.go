package api

import "testing"

// A restore result prints on one line. A failure with a hint gets a
// second line that says what to try, for `host restore` and the boot log.
func TestRestoreResultText(t *testing.T) {
	ok := RestoreResult{ID: "abc12345", Name: "app", Action: "restored", Why: "resumed its own session s1"}
	if got, want := ok.Text(), "abc12345   app                  restored  resumed its own session s1\n"; got != want {
		t.Errorf("Text() = %q, want %q", got, want)
	}
	failed := RestoreResult{ID: "abc12345", Name: "app", Action: "failed", Why: "app did not start again", Hint: "Start a new session."}
	want := "abc12345   app                  failed    app did not start again\n    Try: Start a new session.\n"
	if got := failed.Text(); got != want {
		t.Errorf("Text() = %q, want %q", got, want)
	}
}
