package e2b

import (
	"testing"
	"time"
)

// waitFor polls until ok, or fails the test.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen in 30s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
