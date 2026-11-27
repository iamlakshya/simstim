package client

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type stateFDCapability interface{ Fd() uintptr }

func TestStateFDViewLifetimeAndMethodSets(t *testing.T) {
	stateFDTestHome(t)
	var retained io.ReaderAt
	var writer io.Writer
	err := withStateFileFD(t.Context(), nil, "facts.json", true, func(view stateFileView) (stateFileReplace, error) {
		if view.Exists || view.Size != 0 || view.ReaderAt == nil {
			t.Fatal("missing view is not a nonnil empty source")
		}
		retained = view.ReaderAt
		if _, ok := retained.(io.Closer); ok {
			t.Fatal("view exposes Close")
		}
		if _, ok := retained.(stateFDCapability); ok {
			t.Fatal("view exposes raw descriptor")
		}
		if _, ok := retained.(io.Writer); ok {
			t.Fatal("view exposes mutation")
		}
		if _, err := retained.ReadAt(make([]byte, 1), 0); err != io.EOF {
			t.Fatalf("missing source = %v,want EOF", err)
		}
		return func(ctx context.Context, w io.Writer) error {
			writer = w
			if _, ok := writer.(io.Closer); ok {
				t.Fatal("stream exposes Close")
			}
			if _, ok := writer.(stateFDCapability); ok {
				t.Fatal("stream exposes raw descriptor")
			}
			if _, err := retained.ReadAt(make([]byte, 1), 0); err != io.EOF {
				t.Fatalf("view revoked between change and stream: %v", err)
			}
			_, err := w.Write([]byte("committed"))
			return err
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := retained.ReadAt(nil, 0); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("post-return read not revoked: %v", err)
	}
	if _, err := writer.Write(nil); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("post-return write not revoked: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(StateDir(), "facts.json")); err != nil || string(got) != "committed" {
		t.Fatalf("retained writer changed data: %q,%v", got, err)
	}
}

func TestStateFDCallbackErrorIdentitySurvivesBothPhases(t *testing.T) {
	stateFDTestHome(t)
	stateFDWrite(t, nil, "facts.json", []byte("old"))
	for _, stage := range []string{"change", "stream"} {
		marker := errors.New(stage + " callback marker")
		err := withStateFileFD(t.Context(), nil, "facts.json", true, func(stateFileView) (stateFileReplace, error) {
			if stage == "change" {
				return nil, marker
			}
			return func(context.Context, io.Writer) error { return marker }, nil
		})
		if err != marker {
			t.Fatalf("%s callback identity changed: %v", stage, err)
		}
	}
}

type stateFDHeldRead struct{ entered, release chan struct{} }

func (r stateFDHeldRead) ReadAt(p []byte, _ int64) (int, error) {
	close(r.entered)
	<-r.release
	p[0] = 'x'
	return 1, nil
}

func TestStateFDRevocationDrainsActiveRead(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	view := &stateFDView{ctx: ctx, r: stateFDHeldRead{entered: entered, release: release}}
	read := make(chan error, 1)
	ended := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() {
		unblock()
		select {
		case <-ended:
		case <-time.After(time.Second):
			t.Error("owned read fixture did not stop")
		}
	})
	go func() { defer close(ended); _, err := view.ReadAt(make([]byte, 1), 0); read <- err }()
	<-entered
	revoked := make(chan struct{})
	go func() { view.revoke(); close(revoked) }()
	select {
	case <-revoked:
		t.Fatal("view revoked before the active read drained")
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	unblock()
	if err := <-read; !errors.Is(err, context.Canceled) {
		t.Fatalf("active read lost parent cancellation: %v", err)
	}
	select {
	case <-revoked:
	case <-time.After(time.Second):
		t.Fatal("revocation did not join its active read")
	}
	if _, err := view.ReadAt(make([]byte, 1), 0); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("revoked view touched source: %v", err)
	}
}

func TestStateFDMissingRootAndFileAreRechecked(t *testing.T) {
	for _, kind := range []string{"directory", "file"} {
		t.Run(kind, func(t *testing.T) {
			stateFDTestHome(t)
			if kind == "file" {
				stateFDWrite(t, nil, "other.json", []byte("kept"))
			}
			err := withStateFileFD(t.Context(), nil, "facts.json", false, func(view stateFileView) (stateFileReplace, error) {
				if view.Exists {
					t.Fatal("missing fixture unexpectedly exists")
				}
				if err := os.MkdirAll(StateDir(), 0700); err != nil {
					return nil, err
				}
				return nil, os.WriteFile(filepath.Join(StateDir(), "facts.json"), []byte("foreign"), 0600)
			})
			if err == nil {
				t.Fatal("missing-to-created read returned authority")
			}
		})
	}
}
