package bundle_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/bundle"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/diag"
)

// gzipOf is n MiB of pattern, gzipped: a small file that unzips to much.
func gzipOf(t *testing.T, pattern []byte, n int) []byte {
	t.Helper()
	var gz bytes.Buffer
	z := gzip.NewWriter(&gz)
	chunk := bytes.Repeat(pattern, (1<<20)/len(pattern))
	for range n {
		if _, err := z.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return gz.Bytes()
}

// A profile from a host can be a bomb: a small gzip of many tiny fields.
// The reader refuses it before it holds much memory.
func TestAProfileBombIsRefused(t *testing.T) {
	for name, c := range map[string]struct {
		pattern []byte
		mib     int
	}{
		"64 MiB of zeros":                 {[]byte{0, 0}, 64},
		"64 MiB of fields":                {[]byte{0x08, 0x00}, 64},
		"16 MiB of empty strings":         {[]byte{0x32, 0x00}, 16},
		"16 MiB of empty mapping entries": {[]byte{0x1a, 0x00}, 16},
	} {
		data := gzipOf(t, c.pattern, c.mib)
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		_, _, err := bundle.RewriteProfile(data, func(s string) string { return s })
		runtime.ReadMemStats(&after)
		if err == nil {
			t.Errorf("%s (%d bytes gzipped) was read", name, len(data))
		}
		alloc := after.TotalAlloc - before.TotalAlloc
		t.Logf("%s: %d MiB", name, alloc>>20)
		if alloc > 160<<20 {
			t.Errorf("%s made the reader take %d MiB", name, alloc>>20)
		}
	}
}

// A bundle that stops, as at Ctrl+C or when the launcher quits, leaves no
// file, and says that it stopped.
func TestAStoppedBundleLeavesNoFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if err := diag.WriteFile(filepath.Join(client.DebugDir(), "client-20261016-000000-1.jsonl"), []byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := t.TempDir()
	_, err := bundle.Write(ctx, bundle.Options{Dir: dir})
	var ue *client.UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "stopped before it was complete") || !strings.Contains(ue.Hint, "debug bundle") {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("the folder holds %v", entries)
	}
}

// Each name in the archive is a plain path inside it, of printable ASCII,
// so that unpacking it writes nowhere else, and printing it sends no
// control code to the terminal.
func TestTheArchiveTakesOnlyPlainNames(t *testing.T) {
	for _, n := range []string{"../../.zshrc", "/etc/evil", "host/\x1b]52;c;ZWNobyBoaQ==\x07x", "host/../x", "host//x", "host/x/", "", ".", "host/.."} {
		if bundle.AddEntry(t, n) == nil {
			t.Errorf("the archive took the name %q", n)
		}
	}
	for _, n := range []string{"README.txt", "laptop/doctor.txt", "host/sessions/abcd2345/debug/heap-20261016-185712.pprof"} {
		if err := bundle.AddEntry(t, n); err != nil {
			t.Errorf("the archive refused %q: %v", n, err)
		}
	}
}
