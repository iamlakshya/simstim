package hosttest

import (
	"testing"
	"testing/synctest"
)

// Screen owns the emulator and the reader that drains its query replies.
// Returning from the bubble requires that reader to have ended too.
func TestScreenClosesAndJoinsItsQueryReader(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lines, alt := Screen([]byte("hello\x1b[6n"), 10, 2)
		if alt || len(lines) != 2 || lines[0] != "hello" || lines[1] != "" {
			t.Fatalf("query screen = %q, alternate %v", lines, alt)
		}
	})
}
