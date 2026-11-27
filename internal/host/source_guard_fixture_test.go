//go:build linux || darwin

package host

import (
	"os"
	"path/filepath"
	"testing"
)

func sourceGuardFixture(t *testing.T) (root, marker string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	marker = filepath.Join(root, "git-invoked")
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$FILE_DOOR_GIT_MARKER\"\nprintf fixture\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("FILE_DOOR_GIT_MARKER", marker)
	return root, marker
}
