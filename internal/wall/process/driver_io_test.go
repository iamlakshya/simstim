package process

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failedSpecWriter struct {
	n            int
	write, close error
}

func (w failedSpecWriter) Write([]byte) (int, error) { return w.n, w.write }
func (w failedSpecWriter) Close() error              { return w.close }

func TestPreparedSpecPreservesWriteAndCloseFailures(t *testing.T) {
	writeErr, closeErr := errors.New("write failed"), errors.New("close failed")
	for _, tc := range []struct {
		name   string
		writer failedSpecWriter
		want   error
	}{
		{"write and close", failedSpecWriter{1, writeErr, closeErr}, writeErr},
		{"short write and close", failedSpecWriter{1, nil, closeErr}, io.ErrShortWrite},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := writePreparedSpec(tc.writer, []byte("complete"))
			if !errors.Is(err, tc.want) || !errors.Is(err, closeErr) {
				t.Fatalf("lost write/close cause: %v", err)
			}
		})
	}
}

func TestPreparedSpecRequiresOneDocumentAndWhitespaceEOF(t *testing.T) {
	for _, tail := range []string{"", " \n\r\t", "{}", "null", "x", strings.Repeat(" ", 8192) + "[]"} {
		t.Run(strings.ReplaceAll(tail[:min(12, len(tail))], "\n", "newline"), func(t *testing.T) {
			name := filepath.Join(t.TempDir(), "spec")
			if err := os.WriteFile(name, []byte(`{"ID":"one"}`+tail), 0600); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(name)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			spec, err := readPreparedSpec(f)
			valid := strings.TrimSpace(tail) == ""
			if valid && (err != nil || spec.ID != "one") || !valid && err == nil {
				t.Fatalf("tail accepted=%v, spec=%+v err=%v", valid, spec, err)
			}
		})
	}
}

func TestPreparedTempCleanupRefusesReplacement(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	f, err := root.OpenFile("process.json.new", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	want, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := root.Rename("process.json.new", "original"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "process.json.new"), []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := removePreparedTemp(root, want); err == nil {
		t.Fatal("removed a replacement")
	}
	got, err := os.ReadFile(filepath.Join(dir, "process.json.new"))
	if err != nil || string(got) != "replacement" {
		t.Fatalf("replacement changed: %q %v", got, err)
	}
	if err := root.Remove("process.json.new"); err != nil {
		t.Fatal(err)
	}
	if err := root.Rename("original", "process.json.new"); err != nil {
		t.Fatal(err)
	}
	if err := removePreparedTemp(root, want); err != nil {
		t.Fatal(err)
	}
}
