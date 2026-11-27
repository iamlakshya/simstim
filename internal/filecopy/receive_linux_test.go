//go:build linux

package filecopy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"testing"
)

type receiveObserver struct {
	*bytes.Reader
	observe func()
	onEnd   func()
}

func (r *receiveObserver) Read(data []byte) (int, error) {
	if r.observe != nil {
		r.observe()
	}
	n, err := r.Reader.Read(data)
	if errors.Is(err, io.EOF) && r.onEnd != nil {
		r.onEnd()
		r.onEnd = nil
	}
	return n, err
}

func receiveNames(t *testing.T, root *os.Root) []os.DirEntry {
	t.Helper()
	file, err := root.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := file.ReadDir(-1)
	err = errors.Join(err, file.Close())
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestReceiveFilePublishesLiveAnonymousStage(t *testing.T) {
	root, _ := batchRoot(t)
	data := []byte("checked ordinary bytes")
	sum := sha256.Sum256(data)
	input := &receiveObserver{Reader: bytes.NewReader(data), observe: func() {
		if entries := receiveNames(t, root); len(entries) != 0 {
			t.Fatalf("receive exposed a name before publication: %v", entries)
		}
	}}
	if err := ReceiveFile(t.Context(), root, "notes", int64(len(data)), hex.EncodeToString(sum[:]), false, input); err != nil {
		t.Fatal(err)
	}
	actual, err := root.ReadFile("notes")
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatalf("published bytes = %q %v", actual, err)
	}
	info, err := root.Lstat("notes")
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0644 {
		t.Fatalf("published type or mode = %v %v", info, err)
	}
	if entries := receiveNames(t, root); len(entries) != 1 || entries[0].Name() != "notes" {
		t.Fatalf("receive left transient names: %v", entries)
	}
}

func TestReceiveFileAnonymousStagePreservesLateDestination(t *testing.T) {
	root, _ := batchRoot(t)
	data := []byte("replacement")
	sum := sha256.Sum256(data)
	input := &receiveObserver{Reader: bytes.NewReader(data), onEnd: func() {
		file, err := root.OpenFile("notes", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = file.Write([]byte("planted"))
		if err = errors.Join(err, file.Close()); err != nil {
			t.Fatal(err)
		}
	}}
	var refused *Error
	if err := ReceiveFile(t.Context(), root, "notes", int64(len(data)), hex.EncodeToString(sum[:]), false, input); !errors.As(err, &refused) || refused.Code != "cp_exists" {
		t.Fatalf("late destination refusal = %v", err)
	}
	actual, err := root.ReadFile("notes")
	if err != nil || string(actual) != "planted" {
		t.Fatalf("late destination changed = %q %v", actual, err)
	}
	if entries := receiveNames(t, root); len(entries) != 1 || entries[0].Name() != "notes" {
		t.Fatalf("receive removed a foreign name or left staging: %v", entries)
	}
}

func TestReceiveFileCancellationBeforePublishLeavesNoName(t *testing.T) {
	root, _ := batchRoot(t)
	data := []byte("checked bytes")
	sum := sha256.Sum256(data)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	input := &receiveObserver{Reader: bytes.NewReader(data), onEnd: cancel}
	if err := ReceiveFile(ctx, root, "notes", int64(len(data)), hex.EncodeToString(sum[:]), false, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	if entries := receiveNames(t, root); len(entries) != 0 {
		t.Fatalf("cancelled receive left a name: %v", entries)
	}
}
