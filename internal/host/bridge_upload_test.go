package host_test

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// waitTill waits until ok, for 10 seconds at most.
func waitTill(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !ok(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%s never came", what)
		}
	}
}

// An upload that its client gave up after a chunk, as at a cancel, leaves
// nothing on the host: when its connection closes, when the connection
// begins another upload, and when it waits too long for its next frame on a
// connection that stays. Its temporary file goes, closed, and so does its
// state: its end then gets "unknown upload".
func TestAnUploadWithNoEndLeavesNothing(t *testing.T) {
	hosttest.New(t)
	s := start(t, "paste", "sleep", "60")
	temps := func() int {
		matches, _ := filepath.Glob(filepath.Join(paths.PasteDir(s.ID), ".upload-*"))
		return len(matches)
	}
	begin := func(b *rawBridge, id uint64) {
		t.Helper()
		b.sendJSON(proto.TUploadBegin, proto.UploadBegin{ID: id, Session: s.ID, Name: "a.png", Size: 10})
		b.send(proto.TUploadChunk, proto.EncodeChunk(id, []byte("01234")))
		b.send(proto.TPing, nil)
		b.next(proto.TPong)
	}
	gone := func(b *rawBridge, id uint64) bool {
		t.Helper()
		b.sendJSON(proto.TUploadEnd, proto.UploadEnd{ID: id})
		r := b.response(id)
		return !r.OK && r.Error != nil && r.Error.Message == "unknown upload"
	}

	t.Run("its connection closes", func(t *testing.T) {
		b := newRawBridge(t)
		begin(b, 1)
		waitTill(t, "the upload's file", func() bool { return temps() == 1 })
		_ = b.in.Close()
		waitTill(t, "the end of the upload's file", func() bool { return temps() == 0 })
	})
	t.Run("the next upload begins", func(t *testing.T) {
		b := newRawBridge(t)
		begin(b, 1)
		begin(b, 2)
		if !gone(b, 1) {
			t.Error("the first upload is still there")
		}
		if n := temps(); n != 1 {
			t.Errorf("%d upload files, want the second one alone", n)
		}
	})
	t.Run("it waits too long", func(t *testing.T) {
		defer host.SetUploadIdle(100 * time.Millisecond)()
		b := newRawBridge(t)
		begin(b, 1)
		waitTill(t, "the end of the upload's file", func() bool { return temps() == 0 })
		if !gone(b, 1) {
			t.Error("the upload is still there after it waited too long")
		}
	})
}

// A bridge that ends itself when its client goes quiet, as on a transport
// that does not end it when the connection drops (--idle-exit), leaves no
// upload behind either: the bridge is this test binary, run as a child,
// whose input stays open with nothing more on it after an upload's first
// chunk.
func TestAnIdleBridgeLeavesNoUpload(t *testing.T) {
	hosttest.New(t)
	s := start(t, "paste", "sleep", "60")
	temps := func() int {
		matches, _ := filepath.Glob(filepath.Join(paths.PasteDir(s.ID), ".upload-*"))
		return len(matches)
	}
	cmd := exec.Command(os.Args[0], "bridge", "--idle-exit", "1")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	defer func() {
		_ = in.Close()
		select {
		case <-done:
		default:
			_ = cmd.Process.Kill()
			<-done
		}
	}()
	w := proto.NewWriter(in)
	if err := w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Role: "client"}); err != nil {
		t.Fatal(err)
	}
	if f, err := proto.Read(bufio.NewReader(out)); err != nil || f.Type != proto.THello {
		t.Fatalf("the bridge's hello: %v, %v", f.Type, err)
	}
	_ = w.WriteJSON(proto.TUploadBegin, proto.UploadBegin{ID: 1, Session: s.ID, Name: "a.png", Size: 10})
	_ = w.Write(proto.TUploadChunk, proto.EncodeChunk(1, []byte("01234")))
	waitTill(t, "the upload's file", func() bool { return temps() == 1 })
	select {
	case <-done:
		if waitErr != nil {
			t.Errorf("the idle bridge exited with %v", waitErr)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the bridge did not end when its client went quiet")
	}
	if n := temps(); n != 0 {
		t.Errorf("the idle bridge left %d upload files", n)
	}
}

// ReclaimBridgesUploads waits for in-flight cleanup that has already removed
// an upload from b.uploads before deleting or finalising its file on disk.
func TestReclaimBridgesUploadsWaitsForInFlightCleanup(t *testing.T) {
	hosttest.New(t)
	s := start(t, "paste", "sleep", "60")
	temps := func() int {
		matches, _ := filepath.Glob(filepath.Join(paths.PasteDir(s.ID), ".upload-*"))
		return len(matches)
	}

	t.Run("idle timer drop", func(t *testing.T) {
		defer host.SetUploadIdle(20 * time.Millisecond)()
		untracked, release := make(chan struct{}), make(chan struct{})
		var releaseOnce sync.Once
		closeRelease := func() { releaseOnce.Do(func() { close(release) }) }
		defer closeRelease()
		var once sync.Once
		defer host.SetAfterUploadUntracked(func() {
			once.Do(func() {
				close(untracked)
				<-release
			})
		})()
		atWait := make(chan struct{})
		var waitOnce sync.Once
		defer host.SetBeforeUploadReclaimWait(func() {
			waitOnce.Do(func() { close(atWait) })
		})()

		b := newRawBridge(t)
		b.sendJSON(proto.TUploadBegin, proto.UploadBegin{ID: 1, Session: s.ID, Name: "a.png", Size: 10})
		b.send(proto.TUploadChunk, proto.EncodeChunk(1, []byte("01234")))
		select {
		case <-untracked:
		case <-time.After(5 * time.Second):
			t.Fatal("idle timer never untracked the upload")
		}
		if n := temps(); n != 1 {
			t.Fatalf("expected 1 temp file before drop completes, got %d", n)
		}

		var released, premature atomic.Bool
		reclaimed := make(chan struct{})
		go func() {
			host.ReclaimBridgesUploads()
			if !released.Load() {
				premature.Store(true)
			}
			close(reclaimed)
		}()
		select {
		case <-atWait:
		case <-time.After(5 * time.Second):
			t.Fatal("ReclaimBridgesUploads never reached uploadsWG.Wait")
		}
		for range 64 {
			runtime.Gosched()
		}
		select {
		case <-reclaimed:
			t.Fatal("ReclaimBridgesUploads returned while .upload-* removal was still in flight")
		default:
		}
		if n := temps(); n != 1 {
			t.Errorf("expected temp file to still exist while drop is held, got %d", n)
		}
		released.Store(true)
		closeRelease()
		select {
		case <-reclaimed:
		case <-time.After(5 * time.Second):
			t.Fatal("ReclaimBridgesUploads did not finish after release")
		}
		if premature.Load() {
			t.Fatal("ReclaimBridgesUploads returned before in-flight drop was released")
		}
		if n := temps(); n != 0 {
			t.Errorf("ReclaimBridgesUploads returned with %d temp files still on disk", n)
		}
	})

	t.Run("uploadEnd removal", func(t *testing.T) {
		untracked, release := make(chan struct{}), make(chan struct{})
		var releaseOnce sync.Once
		closeRelease := func() { releaseOnce.Do(func() { close(release) }) }
		defer closeRelease()
		var once sync.Once
		defer host.SetAfterUploadUntracked(func() {
			once.Do(func() {
				close(untracked)
				<-release
			})
		})()
		atWait := make(chan struct{})
		var waitOnce sync.Once
		defer host.SetBeforeUploadReclaimWait(func() {
			waitOnce.Do(func() { close(atWait) })
		})()

		b := newRawBridge(t)
		b.sendJSON(proto.TUploadBegin, proto.UploadBegin{ID: 2, Session: s.ID, Name: "b.png", Size: 10})
		b.send(proto.TUploadChunk, proto.EncodeChunk(2, []byte("01234")))
		b.sendJSON(proto.TUploadEnd, proto.UploadEnd{ID: 2, SHA256: "bad"})
		select {
		case <-untracked:
		case <-time.After(5 * time.Second):
			t.Fatal("uploadEnd never untracked the upload")
		}
		if n := temps(); n != 1 {
			t.Fatalf("expected 1 temp file before uploadEnd cleanup completes, got %d", n)
		}

		var released, premature atomic.Bool
		reclaimed := make(chan struct{})
		go func() {
			host.ReclaimBridgesUploads()
			if !released.Load() {
				premature.Store(true)
			}
			close(reclaimed)
		}()
		select {
		case <-atWait:
		case <-time.After(5 * time.Second):
			t.Fatal("ReclaimBridgesUploads never reached uploadsWG.Wait")
		}
		for range 64 {
			runtime.Gosched()
		}
		select {
		case <-reclaimed:
			t.Fatal("ReclaimBridgesUploads returned while uploadEnd disk cleanup was still in flight")
		default:
		}
		released.Store(true)
		closeRelease()
		select {
		case <-reclaimed:
		case <-time.After(5 * time.Second):
			t.Fatal("ReclaimBridgesUploads did not finish after release")
		}
		if premature.Load() {
			t.Fatal("ReclaimBridgesUploads returned before uploadEnd cleanup was released")
		}
		if n := temps(); n != 0 {
			t.Errorf("ReclaimBridgesUploads returned with %d temp files still on disk", n)
		}
	})
}

// Concurrent idle reclamations, upload endings and watchdog reclamation leave
// zero .upload-* files on disk at the moment ReclaimBridgesUploads returns.
func TestConcurrentUploadReclamationLeavesNoTempFiles(t *testing.T) {
	hosttest.New(t)
	s := start(t, "paste", "sleep", "60")
	var untrackedCount atomic.Int32
	defer host.SetAfterUploadUntracked(func() {
		untrackedCount.Add(1)
	})()
	temps := func() int {
		matches, _ := filepath.Glob(filepath.Join(paths.PasteDir(s.ID), ".upload-*"))
		return len(matches)
	}
	payload := []byte("hello-upload")
	sum := sha256.Sum256(payload)
	hexSum := hex.EncodeToString(sum[:])

	bridges := make([]*rawBridge, 8)
	for i := range bridges {
		bridges[i] = newRawBridge(t)
	}
	// Seed active unfinished uploads on two bridges while the default idle
	// timeout is active so temp files stay on disk until ReclaimBridgesUploads.
	for i := range 2 {
		id := uint64(9000 + i)
		bridges[i].sendJSON(proto.TUploadBegin, proto.UploadBegin{ID: id, Session: s.ID, Name: "seed.png", Size: int64(len(payload))})
		bridges[i].send(proto.TUploadChunk, proto.EncodeChunk(id, payload))
	}
	waitTill(t, "seeded active upload temp files", func() bool { return temps() >= 2 })

	// Shorten idle expiry for the concurrent churn workers.
	defer host.SetUploadIdle(2 * time.Millisecond)()

	var wg sync.WaitGroup
	for i, b := range bridges[2:] {
		wg.Add(1)
		go func(idx int, b *rawBridge) {
			defer wg.Done()
			for j := range 25 {
				id := uint64(idx*100 + j + 1)
				b.sendJSON(proto.TUploadBegin, proto.UploadBegin{ID: id, Session: s.ID, Name: "c.png", Size: int64(len(payload))})
				b.send(proto.TUploadChunk, proto.EncodeChunk(id, payload))
				switch j % 3 {
				case 0:
					b.sendJSON(proto.TUploadEnd, proto.UploadEnd{ID: id, SHA256: hexSum})
				case 1:
					b.sendJSON(proto.TUploadEnd, proto.UploadEnd{ID: id, SHA256: "mismatch"})
				case 2:
					runtime.Gosched()
				}
			}
		}(i, b)
	}
	waitTill(t, "concurrent upload reclamations in flight", func() bool {
		return untrackedCount.Load() >= 4
	})
	host.ReclaimBridgesUploads()
	if n := temps(); n != 0 {
		t.Fatalf("ReclaimBridgesUploads returned with %d .upload-* files on disk", n)
	}
	wg.Wait()
	host.ReclaimBridgesUploads()
	if n := temps(); n != 0 {
		t.Fatalf("final ReclaimBridgesUploads left %d .upload-* files on disk", n)
	}
}
