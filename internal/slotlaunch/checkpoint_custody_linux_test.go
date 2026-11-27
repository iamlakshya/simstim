//go:build linux

package slotlaunch

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestCheckpointCustodyCannotOriginateFromZeroSource(t *testing.T) {
	var source NativeSourceV1
	if retained, err := source.RetainCheckpointCustodyV1(t.Context()); err == nil || retained != nil {
		t.Fatal("a zero source issued checkpoint custody")
	}
	var custody CheckpointCustodyV1
	if backend, err := custody.CheckpointBackendV1(t.Context()); err == nil || backend != nil {
		t.Fatal("a public zero custody issued a native checkpoint backend")
	}
}

// This exercises descriptor cleanup only. These pipes are no native source
// and cannot pass any running/stopped producer or checkpoint operation guard.
func TestCheckpointCustodyCloseJoinsAndClosesOnlyItsDuplicates(t *testing.T) {
	original, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	defer writer.Close()
	duplicate, err := dupFile(original)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	custody := &CheckpointCustodyV1{original: &NativeSourceV1{}, source: &NativeSourceV1{files: []*os.File{duplicate}}, ctx: ctx, cancel: cancel}
	done, err := custody.begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- custody.Close() }()
	select {
	case err := <-closed:
		done()
		t.Fatalf("custody closed before its actual operation joined: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	if _, err := duplicate.Stat(); err != nil {
		done()
		t.Fatal("an active borrowed descriptor closed", err)
	}
	done()
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if _, err := original.Stat(); err != nil {
		t.Fatal("custody closed its original source's descriptor", err)
	}
	if _, err := duplicate.Stat(); err == nil {
		t.Fatal("custody retained its own duplicate after close")
	}
	if _, err := custody.begin(t.Context()); err == nil {
		t.Fatal("closed custody accepted another operation")
	}
}
