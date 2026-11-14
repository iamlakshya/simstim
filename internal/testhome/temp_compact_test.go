package testhome

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCompactTemporaryNamesStayUniqueAndPrivate(t *testing.T) {
	base := t.TempDir()
	seen := map[string]bool{}
	for range 100 {
		dir, err := compactTemp(base, "th", compactSuffix)
		if err != nil {
			t.Fatal(err)
		}
		if seen[dir] || len(filepath.Base(dir)) != len("th")+compactSuffixLen || !strings.HasPrefix(filepath.Base(dir), "th") {
			t.Fatalf("temporary names collided or lost their compact provenance: %s", dir)
		}
		seen[dir] = true
		st, err := os.Stat(dir)
		if err != nil || !st.IsDir() || runtime.GOOS != "windows" && st.Mode().Perm() != 0o700 {
			t.Fatalf("the temporary directory is not private: %v, %v", st, err)
		}
	}
}

func TestCompactTemporaryNamesLeaveForeignCollisionsAlone(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "thAAAAAAA")
	if err := os.WriteFile(file, []byte("foreign file"), 0o600); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(base, "thBBBBBBB")
	if err := os.Mkdir(foreign, 0o700); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(foreign, "sentinel")
	if err := os.WriteFile(child, []byte("foreign directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileBefore, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	dirBefore, err := os.Stat(foreign)
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	dir, err := compactTemp(base, "th", func() string {
		values := []string{"AAAAAAA", "BBBBBBB", "CCCCCCC"}
		if attempts >= len(values) {
			t.Fatal("the exclusive creator retried after a free name")
		}
		value := values[attempts]
		attempts++
		return value
	})
	if err != nil || dir != filepath.Join(base, "thCCCCCCC") || attempts != 3 {
		t.Fatalf("exclusive creation = %q, %v after %d attempts", dir, err, attempts)
	}
	for _, fixture := range []struct {
		path, content string
		before        os.FileInfo
	}{{file, "foreign file", fileBefore}, {foreign, "foreign directory", dirBefore}} {
		after, err := os.Stat(fixture.path)
		if err != nil || !os.SameFile(fixture.before, after) {
			t.Fatalf("a collision replaced a foreign path: %s, %v", fixture.path, err)
		}
		read := fixture.path
		if after.IsDir() {
			read = filepath.Join(read, "sentinel")
		}
		if got, err := os.ReadFile(read); err != nil || string(got) != fixture.content {
			t.Fatalf("a collision changed foreign content: %q, %v", got, err)
		}
	}
}

func TestCompactTemporaryNamesStopAfterTheCollisionBound(t *testing.T) {
	base := t.TempDir()
	foreign := filepath.Join(base, "thAAAAAAA")
	if err := os.Mkdir(foreign, 0o700); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	dir, err := compactTemp(base, "th", func() string { attempts++; return "AAAAAAA" })
	if dir != "" || !errors.Is(err, os.ErrExist) || attempts != 10000 {
		t.Fatalf("collision exhaustion = %q, %v after %d attempts", dir, err, attempts)
	}
	if st, err := os.Stat(foreign); err != nil || !st.IsDir() {
		t.Fatalf("collision exhaustion removed a foreign directory: %v", err)
	}
}
