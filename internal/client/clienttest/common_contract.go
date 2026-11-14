package clienttest

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// WriteScript writes an executable script at path, for a test to run as a
// fake program. Another process writes it, so this one never holds the
// file open for writing: a process that this one starts at that moment
// would keep the open file, and the script's exec would fail with "text
// file busy".
func WriteScript(t testing.TB, path, body string) {
	t.Helper()
	src := path + ".src"
	if err := os.WriteFile(src, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "cp", src, path).CombinedOutput(); err != nil {
		t.Fatalf("write the script %s: %v: %s", path, err, out)
	}
}

// Requests lists the ops of the requests that reached the bridge, in
// order.
func (t *Transport) Requests() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.requests...)
}
