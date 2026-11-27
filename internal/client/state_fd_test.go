package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stateFDTestHome(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	values := map[string]string{"HOME": "home", "USERPROFILE": "home", "XDG_CONFIG_HOME": "config", "XDG_STATE_HOME": "state", "XDG_CACHE_HOME": "cache", "XDG_DATA_HOME": "data", "XDG_RUNTIME_DIR": "run", "XDG_CONFIG_DIRS": "config-dirs", "XDG_DATA_DIRS": "data-dirs", "GNUPGHOME": "gpg"}
	for key, leaf := range values {
		path := filepath.Join(root, leaf)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, path)
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("SSH_AGENT_PID", "")
}

func stateFDWrite(t *testing.T, subdirs []string, name string, data []byte) {
	t.Helper()
	stateFDWriteContext(t, t.Context(), subdirs, name, data)
}

func stateFDWriteContext(t *testing.T, ctx context.Context, subdirs []string, name string, data []byte) {
	t.Helper()
	if err := withStateFile(ctx, subdirs, name, true, func([]byte, bool) ([]byte, error) { return data, nil }); err != nil {
		t.Fatal(err)
	}
}

func TestStateFDByteAdapterPreservesEmptyWriteAndNoChange(t *testing.T) {
	stateFDTestHome(t)
	name := "facts.json"
	stateFDWrite(t, nil, name, nil)
	path := filepath.Join(StateDir(), name)
	before, err := os.Stat(path)
	if err != nil || before.Size() != 0 {
		t.Fatalf("empty byte result was not published: %v", err)
	}
	for _, callback := range []func(stateFileView) (stateFileReplace, error){
		func(v stateFileView) (stateFileReplace, error) {
			if !v.Exists || v.Size != 0 {
				t.Fatal("empty file lost existence")
			}
			return nil, nil
		},
		func(stateFileView) (stateFileReplace, error) { return nil, errStateNoChange },
	} {
		if err := withStateFileFD(t.Context(), nil, name, true, callback); err != nil {
			t.Fatal(err)
		}
		after, err := os.Stat(path)
		if err != nil || !os.SameFile(before, after) {
			t.Fatalf("no-change result replaced the inode: %v", err)
		}
	}
	stateFDWrite(t, nil, name, []byte("next"))
	if got, err := os.ReadFile(path); err != nil || string(got) != "next" {
		t.Fatalf("byte adapter changed replacement: %q, %v", got, err)
	}
}

func TestStateFDRetainsOldDescriptorThroughPublication(t *testing.T) {
	stateFDTestHome(t)
	name := "facts.json"
	stateFDWrite(t, nil, name, []byte("old"))
	var retained io.ReaderAt
	oldHook := afterStateFilePublish
	defer func() { afterStateFilePublish = oldHook }()
	observed := false
	afterStateFilePublish = func() {
		observed = true
		b := make([]byte, 3)
		if n, err := retained.ReadAt(b, 0); n != 3 || err != nil || string(b) != "old" {
			t.Errorf("source closed or changed before publication completion: %q,%d,%v", b, n, err)
		}
		if got, err := os.ReadFile(filepath.Join(StateDir(), name)); err != nil || string(got) != "new" {
			t.Errorf("published bytes absent: %q,%v", got, err)
		}
	}
	err := withStateFileFD(t.Context(), nil, name, true, func(view stateFileView) (stateFileReplace, error) {
		retained = view.ReaderAt
		return func(ctx context.Context, w io.Writer) error { _, err := w.Write([]byte("new")); return err }, nil
	})
	if err != nil || !observed {
		t.Fatalf("publication witness: %v, observed=%v", err, observed)
	}
	if _, err := retained.ReadAt(make([]byte, 1), 0); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("snapshot descriptor outlived transaction: %v", err)
	}
}

func TestStateFDReadsAndWritesKeepParentCancellation(t *testing.T) {
	stateFDTestHome(t)
	stateFDWrite(t, nil, "facts.json", []byte("old"))
	for _, stage := range []string{"read", "stream"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			err := withStateFileFD(ctx, nil, "facts.json", true, func(view stateFileView) (stateFileReplace, error) {
				if stage == "read" {
					cancel()
					if _, err := view.ReaderAt.ReadAt(make([]byte, 1), 0); !errors.Is(err, context.Canceled) {
						t.Errorf("FD read lost parent cancellation: %v", err)
					}
					return nil, nil
				}
				return func(_ context.Context, w io.Writer) error {
					cancel()
					// Deliberately ignore the error: the engine must still refuse publish.
					_, _ = w.Write([]byte("forbidden"))
					return nil
				}, nil
			})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("%s lost cancellation: %v", stage, err)
			}
			if got, err := os.ReadFile(filepath.Join(StateDir(), "facts.json")); err != nil || string(got) != "old" {
				t.Fatalf("cancel published data: %q,%v", got, err)
			}
			files, _ := filepath.Glob(filepath.Join(StateDir(), ".facts.json.tmp-*"))
			if len(files) != 0 {
				t.Fatalf("cancel left owned temps: %q", files)
			}
		})
	}
}

func TestStateFDStreamingCopiesOpaqueBytesExactly(t *testing.T) {
	stateFDTestHome(t)
	data := bytes.Repeat([]byte("opaque \" future \\ metadata\n"), 4096)
	stateFDWrite(t, nil, "facts.json", data)
	err := withStateFileFD(t.Context(), nil, "facts.json", true, func(view stateFileView) (stateFileReplace, error) {
		return func(ctx context.Context, w io.Writer) error {
			_, err := io.CopyBuffer(w, io.NewSectionReader(view.ReaderAt, 0, view.Size), make([]byte, 32<<10))
			return err
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(StateDir(), "facts.json")); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("opaque stream changed: bytes=%d,error=%v", len(got), err)
	}
}

// This is explicit synthetic fixture ownership BEFORE locally generated facts.
// It does not assert transport/pin/Hello provenance or wrap a remote payload.
func stateFDObserveFixture(t *testing.T, ctx context.Context, h Host) context.Context {
	t.Helper()
	observation, err := ObserveHostStateContext(ctx, h, strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	bound := WithHostStateObservation(ctx, observation)
	bindGeneratedHostStateFixture(t, bound, h, strings.Repeat("a", 32))
	return bound
}
