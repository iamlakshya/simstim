//go:build !windows

package client_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

// This uses the existing real local bridge/holder/PTY fixture. It proves the
// attach boundary and payload, not a native VM or remote SSH deployment.
func TestAttachMalformedConfigStaysReadOnlyBeforeActualPump(t *testing.T) {
	r := newAttachRun(t)
	id := r.session("printf 'READONLY-STREAM\\n'; exec sleep 60")
	if err := os.MkdirAll(client.ConfigDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(client.ConfigDir(), "config.toml"), []byte("[broken\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var warnings atomic.Int32
	ctx := client.WithWarnings(context.Background(), func(code, msg, hint string) {
		if code == "config_ignored" && strings.Contains(msg, "without sending secrets") {
			warnings.Add(1)
		}
	})
	done := r.attach(ctx, id, client.DefaultSettings())
	r.term.WaitFor(rawReady)
	if out := r.term.WaitFor("READONLY-STREAM"); !bytes.Contains(out, []byte("READONLY-STREAM")) {
		t.Fatal("malformed config changed the session payload")
	}
	r.term.Type(detach)
	ended := r.end(done)
	if ended.err != nil || !ended.res.Detached || warnings.Load() != 1 {
		t.Fatal("read-only attach failed or omitted its warning", ended.err, warnings.Load())
	}
	if bytes.Contains(r.term.Output(), []byte("\x1bc")) {
		t.Fatal("read-only attach sent RIS")
	}
}
