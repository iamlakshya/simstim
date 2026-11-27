//go:build darwin || linux

package client

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
)

// This wrapper retains the real open descriptor and native FileInfo. Its
// fault happens on the first actual TOML byte read, after the state tx and
// combined lease have been acquired. It substitutes no inspector or Settings value.
type agentInspectionFaultFile struct {
	configFile
	reader io.ReaderAt
	once   sync.Once
	fault  func() error
	err    error
}

func (f *agentInspectionFaultFile) ReadAt(out []byte, offset int64) (int, error) {
	if offset == 0 {
		f.once.Do(func() { f.err = f.fault() })
		if f.err != nil {
			return 0, f.err
		}
	}
	return f.reader.ReadAt(out, offset)
}

func TestAgentCombinedInspectionRefusesChangedOriginalSourcesAndMode(t *testing.T) {
	for _, target := range []string{"settings", "state", "mode"} {
		t.Run(target, func(t *testing.T) {
			conn, mode := agentCombinedInspectionFixture(t, nil)
			path := settingsPath()
			if target == "state" {
				path = sandboxStatePath(conn.Host)
			}
			var replacement string
			if target != "mode" {
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				replacement = path + ".replacement"
				if err := os.WriteFile(replacement, body, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			originalOpen := openConfig
			fired := false
			openConfig = func(name string) (configFile, error) {
				file, err := originalOpen(name)
				if err != nil || name != settingsPath() {
					return file, err
				}
				reader, ok := file.(io.ReaderAt)
				if !ok {
					_ = file.Close()
					return nil, errAgentSettingsSchema
				}
				return &agentInspectionFaultFile{configFile: file, reader: reader, fault: func() error {
					fired = true
					if target == "mode" {
						mode.cancel()
						return nil
					}
					return os.Rename(replacement, path)
				}}, nil
			}
			t.Cleanup(func() { openConfig = originalOpen })
			err := conn.checkAgentPolicy(t.Context(), projectionSession, mode.inspector)
			if !fired {
				t.Fatal("the source fault did not occur during the real combined inspection")
			}
			if err == nil {
				t.Fatal("a replaced original source or revoked mode returned an agent permit")
			}
			if target == "mode" && !errors.Is(err, context.Canceled) {
				t.Fatalf("mode revocation lost the synchronously observed cancellation: %v", err)
			}
		})
	}
}
