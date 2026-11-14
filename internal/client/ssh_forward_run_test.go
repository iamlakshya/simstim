package client

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
)

func TestRunForwardCleansStreamingDiagnostics(t *testing.T) {
	dir := fakeSSH(t)
	pinFor(t, "box")
	put(t, dir, "hop-stderr", "banner\x1bc\x1b]52;c;clipboard\a\u202e\n")
	put(t, dir, "exit-code", "0")
	var output bytes.Buffer
	started := false
	s := &SSH{Host: Host{Name: "box", SSH: "box.example"}}
	err := s.RunForward(context.Background(), nil, nil, &output, &output, func() {
		started = true
		calls := sshCalls(t, dir)
		found := false
		for _, call := range calls {
			if strings.Contains(call, " -N ") && strings.Contains(call, " -E ") && strings.Contains(call, " -G -- box.example") {
				found = true
			}
		}
		if !found {
			t.Errorf("forward started before its complete config check: %q", calls)
		}
		output.WriteString("started\n")
	})
	if err != nil || !started {
		t.Fatalf("forward = %v, started %v", err, started)
	}
	if got := output.String(); !strings.HasPrefix(got, "started\n") || !strings.Contains(got, "banner") || strings.ContainsAny(got, "\x1b\x07\u202e") {
		t.Fatalf("unclean or out-of-order forward output: %q", got)
	}
}

func TestRunForwardReadsOnlyTheDestinationKeyVerdict(t *testing.T) {
	for _, test := range []struct {
		name   string
		log    bool
		signal bool
		want   bool
	}{{"destination refuses key", true, false, true}, {"hop forges key refusal", false, false, false}, {"signal is not a key verdict", true, true, false}} {
		t.Run(test.name, func(t *testing.T) {
			dir := fakeSSH(t)
			pinFor(t, "box")
			h := Host{Name: "box", SSH: "box.example"}
			stop := "Host key for " + pinAlias(h.Name) + " has changed and you have requested strict checking.\nHost key verification failed.\n"
			if test.log {
				put(t, dir, "stderr", stop)
			} else {
				put(t, dir, "hop-stderr", stop)
				put(t, dir, "stderr", "Permission denied (publickey).\n")
			}
			if test.signal {
				put(t, dir, "kill-self", "")
			}
			var stderr bytes.Buffer
			err := (&SSH{Host: h}).RunForward(context.Background(), nil, nil, nil, &stderr, nil)
			var ue *UserError
			refused := errors.As(err, &ue) && ue.Code == "host_key_changed" && ue.Kind == KindRefused
			if err == nil || refused != test.want {
				t.Fatalf("forward = %#v, want key refusal %v", err, test.want)
			}
			if test.want && ue.Hint == "" {
				t.Fatal("key refusal has no recovery hint")
			}
		})
	}
}

func TestRunForwardKeepsUsefulPrivateLogDiagnostics(t *testing.T) {
	dir := fakeSSH(t)
	pinFor(t, "box")
	put(t, dir, "stderr", "Try: take an unsafe action\n\x1bcbind [127.0.0.1]:45678: Address already in use\n")
	err := (&SSH{Host: Host{Name: "box", SSH: "box.example"}}).RunForward(context.Background(), nil, nil, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "Address already in use") || strings.ContainsAny(err.Error(), "\x1b\x07") || strings.Contains(err.Error(), "Try:") {
		t.Fatalf("forward lost or failed to clean its -E diagnostic: %v", err)
	}
}

func TestRunForwardCompletesPreflightBeforeItsBanner(t *testing.T) {
	for _, failure := range []string{"pin", "config", "log"} {
		t.Run(failure, func(t *testing.T) {
			dir := fakeSSH(t)
			pinFor(t, "box")
			switch failure {
			case "pin":
				if err := forgetPin("box"); err != nil {
					t.Fatal(err)
				}
			case "config":
				put(t, dir, "values", "remoteforward 3000 127.0.0.1:22\n")
			case "log":
				t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
			}
			started := false
			var output bytes.Buffer
			err := (&SSH{Host: Host{Name: "box", SSH: "box.example"}}).RunForward(context.Background(), nil, nil, nil, &output, func() { started = true })
			if err == nil || started || output.Len() != 0 {
				t.Fatalf("preflight = %v, banner %v, output %q", err, started, output.String())
			}
		})
	}
}

func TestRunForwardPreparesPrivateLogBeforeItsBanner(t *testing.T) {
	dir := fakeSSH(t)
	pinFor(t, "box")
	put(t, dir, "exit-code", "0")
	logRoot := t.TempDir()
	t.Setenv("TMPDIR", logRoot)
	started := false
	err := (&SSH{Host: Host{Name: "box", SSH: "box.example"}}).RunForward(context.Background(), nil, nil, nil, nil, func() {
		started = true
		entries, err := os.ReadDir(logRoot)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), brand.Name+"-ssh-") {
				continue
			}
			folder := filepath.Join(logRoot, entry.Name())
			info, err := os.Stat(folder)
			if err != nil || info.Mode().Perm() != 0o700 {
				t.Fatalf("forward log folder = %v, %v", info, err)
			}
			info, err = os.Stat(filepath.Join(folder, "ssh.log"))
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
				t.Fatalf("forward log file = %v, %v", info, err)
			}
			found = true
		}
		if !found {
			t.Fatal("no private destination log exists before the banner")
		}
	})
	if err != nil || !started {
		t.Fatalf("forward = %v, started %v", err, started)
	}
	entries, err := os.ReadDir(logRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("forward left private logs: %v, %v", entries, err)
	}
}

func TestRunForwardHonoursCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := false
	err := (&SSH{}).RunForward(ctx, nil, nil, nil, nil, func() { started = true })
	if !errors.Is(err, context.Canceled) || reflect.TypeOf(err) != reflect.TypeOf(context.Canceled) || started {
		t.Fatalf("forward = %v, banner %v, want cancellation before start", err, started)
	}
}

func TestRunForwardCancellationIsNotAKeyRefusal(t *testing.T) {
	dir := fakeSSH(t)
	pinFor(t, "box")
	put(t, dir, "stderr", "Host key for "+pinAlias("box")+" has changed and you have requested strict checking.\nHost key verification failed.\n")
	script := `#!/bin/sh
case " $* " in *" -G "*|*" -O "*) exit 0;; esac
prev=
for a; do [ "$prev" = -E ] && log=$a; prev=$a; done
cat "$FAKE_SSH/stderr" >> "$log"
printf 'ready\n' >&2
exec sleep 30
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var output bytes.Buffer
	err := (&SSH{Host: Host{Name: "box", SSH: "box.example"}}).RunForward(ctx, nil, nil, nil, forwardCancelWriter{w: &output, cancel: cancel}, nil)
	if !errors.Is(err, context.Canceled) || reflect.TypeOf(err) != reflect.TypeOf(context.Canceled) || !strings.Contains(output.String(), "ready") {
		t.Fatalf("forward = %v, output %q, want cancellation after start", err, output.String())
	}
}

func TestRunForwardSerialisesASharedOutputWriter(t *testing.T) {
	dir := fakeSSH(t)
	pinFor(t, "box")
	script := `#!/bin/sh
case " $* " in *" -G "*|*" -O "*) exit 0;; esac
i=0
while [ "$i" -lt 256 ]; do
  printf 'stdout line with enough text to fill both output pipes\n'
  printf 'stderr line with enough text to fill both output pipes\n' >&2
  i=$((i + 1))
done
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var output forwardConcurrentWriter
	err := (&SSH{Host: Host{Name: "box", SSH: "box.example"}}).RunForward(context.Background(), nil, nil, &output, &output, nil)
	if err != nil || output.overlap.Load() || output.writes.Load() < 2 {
		t.Fatalf("forward = %v, concurrent writer calls %v, writes %d", err, output.overlap.Load(), output.writes.Load())
	}
}

type forwardConcurrentWriter struct {
	inside  atomic.Bool
	overlap atomic.Bool
	writes  atomic.Int64
}

func (w *forwardConcurrentWriter) Write(p []byte) (int, error) {
	if !w.inside.CompareAndSwap(false, true) {
		w.overlap.Store(true)
	}
	w.writes.Add(1)
	runtime.Gosched()
	w.inside.Store(false)
	return len(p), nil
}

type forwardCancelWriter struct {
	w      *bytes.Buffer
	cancel context.CancelFunc
}

func (w forwardCancelWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	w.cancel()
	return n, err
}
