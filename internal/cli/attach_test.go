package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

// writeCache writes the session cache.
func writeCache(t *testing.T, cache string) {
	t.Helper()
	if err := os.MkdirAll(client.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(client.StateDir(), "launcher.json"), []byte(cache), 0o600); err != nil {
		t.Fatal(err)
	}
}

const cachedOne = `{"sessions":{"box":[{"id":"zzz","name":"cached-one","agent":"claude","state":"idle","created_at":"2026-11-10T10:00:00Z","alive":true}]}}`
