package host

import (
	"context"
	"errors"
	"github.com/iamlakshya/simstim/internal/wall"
	"os"
	"path/filepath"
	"testing"
)

func fakeCapSource(t *testing.T) capSource {
	t.Helper()
	dir := isolateCapTest(t)
	return capSource{root: filepath.Join(dir, "root"), os: "linux", arch: "amd64", username: "example", uid: 1000,
		stateDir: filepath.Join(dir, "state"), dataDir: filepath.Join(dir, "data"), incomingDir: filepath.Join(dir, "data", "artefacts", "incoming"),
		lookup: func(string) bool { return false }, run: func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("missing") },
		disk: func(string, string) (int64, string, bool) { return 10 << 30, "xfs", true },
		wallProbes: func(context.Context) []wall.Probe {
			return []wall.Probe{{Driver: "process", Level: wall.Process, Reason: "runtime unavailable", Facts: map[string]string{"userns": "true", "landlock_abi": "6", "seccomp": "true", "runtime_available": "false"}}}
		},
	}
}

func fakeCapFile(t *testing.T, s capSource, path, body string) {
	t.Helper()
	path = s.path(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
