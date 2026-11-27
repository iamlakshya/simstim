//go:build darwin || linux

package client

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func nativeLogTestEnv(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "nl-")
	if err != nil {
		t.Fatal(err)
	}
	created := root
	t.Cleanup(func() { _ = os.RemoveAll(created) })
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	for key, leaf := range map[string]string{
		"HOME": "home", "USERPROFILE": "home", "XDG_CONFIG_HOME": "config", "XDG_DATA_HOME": "data",
		"XDG_STATE_HOME": "state", "XDG_CACHE_HOME": "cache", "XDG_RUNTIME_DIR": "run",
		"XDG_CONFIG_DIRS": "config-dirs", "XDG_DATA_DIRS": "data-dirs", "GNUPGHOME": "gnupg",
		"TMPDIR": "tmp", "TMP": "tmp", "TEMP": "tmp",
	} {
		path := filepath.Join(root, leaf)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, path)
	}
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("SSH_AGENT_PID", "")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(root, "gitconfig-system"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	t.Setenv("GPG_AGENT_INFO", "")
	t.Setenv("GPG_TTY", "")
	return root
}

func nativeTestLog(t *testing.T, ctx context.Context) *agentNativeLog {
	t.Helper()
	log, err := newAgentNativeLog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

func nativeTestWriter(t *testing.T, log *agentNativeLog) *os.File {
	t.Helper()
	writer, err := os.OpenFile(log.Path(), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	return writer
}

func waitNativeLog(t *testing.T, ready func() bool) {
	t.Helper()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !ready() {
		select {
		case <-timeout.C:
			t.Fatal("private native log did not reach its expected state")
		case <-tick.C:
		}
	}
}

func TestAgentNativeLogKeepsTheCompletePrefixAndPrivateRegularInode(t *testing.T) {
	nativeLogTestEnv(t)
	log := nativeTestLog(t, t.Context())
	if err := log.AttachOwner(func(error) {}); err != nil {
		t.Fatal(err)
	}
	fifo, err := os.Lstat(log.Path())
	regular, rerr := log.root.Lstat("transcript.log")
	if err != nil || rerr != nil || fifo.Mode().Type() != os.ModeNamedPipe || fifo.Mode().Perm() != 0o600 ||
		!regular.Mode().IsRegular() || regular.Mode().Perm() != 0o600 || !os.SameFile(regular, log.regularInfo) {
		t.Fatalf("private capture types/ownership: fifo=%+v regular=%+v errors=%v/%v", fifo, regular, err, rerr)
	}
	writer := nativeTestWriter(t, log)
	want := "debug1: first trusted record\r\n" + strings.Repeat("debug1: intermediate record\r\n", 100) + "debug1: last record\r\n"
	if _, err := io.WriteString(writer, want); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := log.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	var got []byte
	if err := log.ScanSnapshot(ctx, func(reader io.Reader) error { var err error; got, err = io.ReadAll(reader); return err }); err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("native capture omitted or replaced prefix bytes: got=%d want=%d", len(got), len(want))
	}
	dir := filepath.Dir(log.Path())
	_ = writer.Close()
	text, err := log.Finish()
	if err != nil || text != want {
		t.Fatalf("complete small diagnostic: bytes=%d error=%v", len(text), err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("finished native log retained its directory: %v", err)
	}
}

func TestAgentNativeLogStartupWriterPreventsPrematureEOF(t *testing.T) {
	nativeLogTestEnv(t)
	log := nativeTestLog(t, t.Context())
	log.mu.Lock()
	var buffer [1]byte
	n, err := log.rawReadLocked(buffer[:])
	ended := log.eof
	log.mu.Unlock()
	if n != 0 || err != nil || ended {
		t.Fatalf("the FIFO ended before the owned process opened it: n=%d err=%v ended=%v", n, err, ended)
	}
	writer := nativeTestWriter(t, log)
	if _, err := io.WriteString(writer, "first output\n"); err != nil {
		t.Fatal(err)
	}
	if err := log.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestAgentNativeLogRejectsIncompletePrefixBeforeACK(t *testing.T) {
	nativeLogTestEnv(t)
	log := nativeTestLog(t, t.Context())
	called := make(chan error, 1)
	if err := log.AttachOwner(func(error) { called <- log.Failure() }); err != nil {
		t.Fatal(err)
	}
	writer := nativeTestWriter(t, log)
	if _, err := io.WriteString(writer, "an incomplete native record"); err != nil {
		t.Fatal(err)
	}
	err := log.Sync(t.Context())
	var refusal *UserError
	if !errors.As(err, &refusal) || refusal.Kind != KindRefused || log.Failure() == nil {
		t.Fatalf("an incomplete prefix was accepted: %v", err)
	}
	select {
	case failure := <-called:
		if failure == nil {
			t.Fatal("owner callback ran before sticky failure publication")
		}
	case <-time.After(time.Second):
		t.Fatal("native log failure did not cancel its owner outside the mutex")
	}
}

func TestAgentNativeLogBoundsLineAndTotalBeforeFurtherProof(t *testing.T) {
	for _, mode := range []string{"line", "total", "nul"} {
		t.Run(mode, func(t *testing.T) {
			nativeLogTestEnv(t)
			log := nativeTestLog(t, t.Context())
			var callbacks atomic.Int32
			if err := log.AttachOwner(func(error) { callbacks.Add(1) }); err != nil {
				t.Fatal(err)
			}
			writer := nativeTestWriter(t, log)
			data := []byte(strings.Repeat("x", agentNativeLogLine+1) + "\n")
			if mode == "total" {
				data = bytes.Repeat([]byte("bounded record\n"), agentNativeLogTotal/len("bounded record\n")+2)
			} else if mode == "nul" {
				data = []byte("native\x00record\n")
			}
			written := make(chan struct{})
			go func() { _, _ = writer.Write(data); close(written) }()
			waitNativeLog(t, func() bool { return log.Failure() != nil && callbacks.Load() == 1 })
			select {
			case <-written:
			case <-time.After(time.Second):
				t.Fatal("capture failure did not interrupt the owned FIFO writer")
			}
			if err := log.ScanSnapshot(t.Context(), func(io.Reader) error { t.Error("failed capture reached verifier"); return nil }); err == nil {
				t.Fatal("a failed native capture could still authorise a proof")
			}
			info, err := log.regular.Stat()
			if err != nil || info.Size() > agentNativeLogTotal || cap(log.pending) > agentNativeLogLine {
				t.Fatalf("capture storage exceeded its bounds: %+v, pending=%d err=%v", info, cap(log.pending), err)
			}
			text, err := log.Finish()
			if text != "" || err == nil || callbacks.Load() != 1 {
				t.Fatalf("resource failure was replaced by diagnostic success: text=%d err=%v callbacks=%d", len(text), err, callbacks.Load())
			}
		})
	}
}

func TestAgentNativeLogLargeSnapshotStreamsButFinishAllocatesNoFullLog(t *testing.T) {
	nativeLogTestEnv(t)
	log := nativeTestLog(t, t.Context())
	if err := log.AttachOwner(func(error) { t.Error("a valid large transcript cancelled its owner") }); err != nil {
		t.Fatal(err)
	}
	writer := nativeTestWriter(t, log)
	first := "debug1: first authoritative record\n"
	data := first + strings.Repeat("debug1: bounded intermediate log record\n", 6000)
	if _, err := io.WriteString(writer, data); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := log.ScanSnapshot(t.Context(), func(reader io.Reader) error {
		buffered := bufio.NewReaderSize(reader, 1024)
		line, err := buffered.ReadString('\n')
		if err != nil || line != first {
			return errors.New("complete prefix was replaced by a tail")
		}
		rest, err := io.Copy(io.Discard, buffered)
		count = int64(len(line)) + rest
		return err
	}); err != nil || count != int64(len(data)) {
		t.Fatalf("streamed prefix was incomplete: count=%d want=%d err=%v", count, len(data), err)
	}
	_ = writer.Close()
	summary, err := log.FinishScan(func(reader io.Reader) error { _, err := io.Copy(io.Discard, reader); return err })
	if err != nil || !summary.Complete || !summary.Classified || summary.Bytes != int64(len(data)) ||
		len(summary.Text) > agentNativeLogDisplayLimit || strings.Contains(summary.Text, "authoritative record") || log.Failure() != nil {
		t.Fatalf("large final classification was silently tailed or fatal: %+v err=%v failure=%v", summary, err, log.Failure())
	}
}

func TestAgentNativeLogDoesNotInventClassificationOrCaptureFailure(t *testing.T) {
	for _, semanticError := range []bool{false, true} {
		t.Run(map[bool]string{false: "no classifier", true: "semantic result"}[semanticError], func(t *testing.T) {
			nativeLogTestEnv(t)
			log := nativeTestLog(t, t.Context())
			if err := log.AttachOwner(func(error) { t.Error("semantic classification cancelled the process") }); err != nil {
				t.Fatal(err)
			}
			writer := nativeTestWriter(t, log)
			if _, err := io.WriteString(writer, "a complete final native record\n"); err != nil {
				t.Fatal(err)
			}
			_ = writer.Close()
			var classifier func(io.Reader) error
			sentinel := errors.New("canonical SSH semantic outcome")
			if semanticError {
				classifier = func(reader io.Reader) error { _, _ = io.Copy(io.Discard, reader); return sentinel }
			}
			summary, err := log.FinishScan(classifier)
			if semanticError {
				if !errors.Is(err, sentinel) || log.Failure() != nil {
					t.Fatalf("semantic error became a capture failure: %v failure=%v", err, log.Failure())
				}
			} else if err != nil || !summary.Complete || summary.Classified {
				t.Fatalf("nil classifier was treated as semantic proof: %+v, %v", summary, err)
			}
		})
	}
}
