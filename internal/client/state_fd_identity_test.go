package client

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestStateFDRefusesReplacedSourceAndPreservesForeignTemp(t *testing.T) {
	for _, replace := range []string{"source", "temp", "lock"} {
		t.Run(replace, func(t *testing.T) {
			stateFDTestHome(t)
			stateFDWrite(t, nil, "facts.json", []byte("old"))
			var foreign string
			err := withStateFileFD(t.Context(), nil, "facts.json", true, func(stateFileView) (stateFileReplace, error) {
				return func(_ context.Context, w io.Writer) error {
					if _, err := w.Write([]byte("next")); err != nil {
						return err
					}
					switch replace {
					case "source":
						foreign = filepath.Join(StateDir(), "facts.json")
					case "lock":
						foreign = filepath.Join(StateDir(), "facts.json.lock")
					case "temp":
						paths, err := filepath.Glob(filepath.Join(StateDir(), ".facts.json.tmp-*"))
						if err != nil || len(paths) != 1 {
							t.Fatalf("temp not owned before swap: %q,%v", paths, err)
						}
						foreign = paths[0]
					}
					if err := os.Rename(foreign, foreign+".original"); err != nil {
						return err
					}
					return os.WriteFile(foreign, []byte("foreign"), 0600)
				}, nil
			})
			if err == nil {
				t.Fatal("replaced source, temp or lock was accepted")
			}
			if got, err := os.ReadFile(foreign); err != nil || string(got) != "foreign" {
				t.Fatalf("foreign replacement changed: %q,%v", got, err)
			}
			if replace != "source" {
				if got, err := os.ReadFile(filepath.Join(StateDir(), "facts.json")); err != nil || string(got) != "old" {
					t.Fatalf("failed write replaced source: %q,%v", got, err)
				}
			}
		})
	}
}

func TestStateFDRefusesRootSwapAndMissingInputReplacement(t *testing.T) {
	for _, kind := range []string{"root", "missing"} {
		t.Run(kind, func(t *testing.T) {
			stateFDTestHome(t)
			if kind == "root" {
				stateFDWrite(t, nil, "facts.json", []byte("old"))
			}
			root := StateDir()
			hook := beforeGrantsStateWrite
			defer func() { beforeGrantsStateWrite = hook }()
			beforeGrantsStateWrite = func() {
				if kind == "root" {
					if err := os.Rename(root, root+".original"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(root, 0700); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(root, "facts.json"), []byte("foreign"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := withStateFileFD(t.Context(), nil, "facts.json", true, func(view stateFileView) (stateFileReplace, error) {
				if kind == "missing" && view.Exists {
					t.Fatal("missing fixture exists")
				}
				return func(_ context.Context, w io.Writer) error { _, err := w.Write([]byte("new")); return err }, nil
			})
			if err == nil {
				t.Fatal("root or missing-source replacement was overwritten")
			}
			if got, err := os.ReadFile(filepath.Join(root, "facts.json")); err != nil || string(got) != "foreign" {
				t.Fatalf("foreign root/file changed: %q,%v", got, err)
			}
		})
	}
}

func TestStateFDReadOnlyCallbackCannotPublishAndNoChangeIsValidated(t *testing.T) {
	stateFDTestHome(t)
	stateFDWrite(t, nil, "facts.json", []byte("old"))
	wrote := false
	if err := withStateFileFD(t.Context(), nil, "facts.json", false, func(stateFileView) (stateFileReplace, error) {
		return func(context.Context, io.Writer) error { wrote = true; return nil }, nil
	}); err != nil || wrote {
		t.Fatalf("read-only transaction published: %v,wrote=%v", err, wrote)
	}
	marker := errors.New("callback failure")
	if err := withStateFile(t.Context(), nil, "facts.json", true, func([]byte, bool) ([]byte, error) { return nil, marker }); !errors.Is(err, marker) {
		t.Fatalf("byte callback error identity changed: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(StateDir(), "facts.json")); err != nil || string(got) != "old" {
		t.Fatalf("callback failure wrote bytes: %q,%v", got, err)
	}
}

func TestStateFDNoChangeStillRefusesSourceReplacement(t *testing.T) {
	stateFDTestHome(t)
	stateFDWrite(t, nil, "facts.json", []byte("old"))
	path := filepath.Join(StateDir(), "facts.json")
	err := withStateFileFD(t.Context(), nil, "facts.json", true, func(stateFileView) (stateFileReplace, error) {
		if err := os.Rename(path, path+".original"); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte("foreign"), 0600); err != nil {
			return nil, err
		}
		return nil, errStateNoChange
	})
	if err == nil {
		t.Fatal("no-change callback accepted replaced source")
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "foreign" {
		t.Fatalf("no-change callback touched foreign source: %q,%v", got, err)
	}
}

func TestStateFDCancelAfterPublicationReportsCommittedBytes(t *testing.T) {
	stateFDTestHome(t)
	stateFDWrite(t, nil, "facts.json", []byte("old"))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	hook := afterStateFilePublish
	defer func() { afterStateFilePublish = hook }()
	afterStateFilePublish = cancel
	err := withStateFileFD(ctx, nil, "facts.json", true, func(stateFileView) (stateFileReplace, error) {
		return func(_ context.Context, w io.Writer) error { _, err := w.Write([]byte("committed")); return err }, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("post-publication cancellation lost: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(StateDir(), "facts.json")); err != nil || string(got) != "committed" {
		t.Fatalf("committed rename was hidden or rolled back: %q,%v", got, err)
	}
	temps, _ := filepath.Glob(filepath.Join(StateDir(), ".facts.json.tmp-*"))
	if len(temps) != 0 {
		t.Fatalf("published transaction retained temps: %q", temps)
	}
}
