package host

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestPendingControlInvalidIDCannotReachLockOrEndpointEffects(t *testing.T) {
	for _, id := range []string{"", "../outside", "bad/name"} {
		t.Run(id, func(t *testing.T) {
			startupTestManifest(t)
			if validID(id) {
				t.Fatal("invalid-ID fixture unexpectedly became a valid session")
			}
			markers := []string{holderLockPath(id), paths.Socket(id), paths.HookSocket(id)}
			foreign := []byte("pre-existing node must remain outside invalid session admission")
			for _, path := range markers {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, foreign, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := RunHolder(id)
			var refusal *proto.Error
			if !errors.As(err, &refusal) || refusal.Code != "not_found" || refusal.Hint == "" {
				t.Fatalf("invalid session reached startup effects: %v", err)
			}
			for _, path := range markers {
				if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, foreign) {
					t.Fatalf("invalid session changed pre-existing %s: %q, %v", path, got, err)
				}
			}
		})
	}
}
