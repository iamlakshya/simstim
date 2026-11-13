//go:build !windows

package client_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
)

// Uploads on one connection take turns, whole: two at once both arrive,
// each as it was sent, though the host drops an unfinished upload when the
// next one begins. The second waits until the first has its answer, and one
// whose context ends while it waits its turn stops at once. An upload that
// a cancel stopped, followed by another, leaves nothing.
func TestUploadsTakeTurns(t *testing.T) {
	h := hosttest.New(t)
	ch, _ := h.ClientHost("box")
	c, err := client.Dial(context.Background(), ch, false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	id := host.NewID()
	temps := func() int {
		matches, _ := filepath.Glob(filepath.Join(paths.PasteDir(id), ".upload-*"))
		return len(matches)
	}
	data := func(b byte) []byte { return bytes.Repeat([]byte{b}, 600<<10) } // three chunks

	inChunkA := make(chan struct{})
	releaseA := make(chan struct{})
	extraChunk := make(chan struct{}, 16)
	var once sync.Once
	restore := client.SetRequestStage(func(stage string) {
		if stage != "upload chunk" {
			return
		}
		held := false
		once.Do(func() {
			held = true
			close(inChunkA)
			select {
			case <-releaseA:
			case <-extraChunk: // another upload reached its chunk without waiting its turn
			}
		})
		if !held {
			select {
			case extraChunk <- struct{}{}:
			default:
			}
		}
	})

	first := make(chan error, 1)
	go func() {
		p, err := c.Upload(context.Background(), id, "a.txt", data('a'))
		if err == nil {
			err = same(p, data('a'))
		}
		first <- err
	}()
	select {
	case <-inChunkA:
	case <-time.After(10 * time.Second):
		t.Fatal("the first upload never reached its first chunk")
	}

	second := make(chan error, 1)
	go func() {
		p, err := c.Upload(context.Background(), id, "b.txt", data('b'))
		if err == nil {
			err = same(p, data('b'))
		}
		second <- err
	}()

	wctx, wcancel := context.WithCancel(context.Background())
	waiting := make(chan error, 1)
	go func() {
		_, err := c.Upload(wctx, id, "w.txt", data('w'))
		waiting <- err
	}()
	time.Sleep(30 * time.Millisecond)
	wcancel()
	select {
	case err := <-waiting:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("an upload cancelled while waiting its turn = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("an upload did not stop when its context ended while waiting its turn")
	}

	close(releaseA)
	for name, ch := range map[string]chan error{"first": first, "second": second} {
		select {
		case err := <-ch:
			if err != nil {
				t.Errorf("the %s of two uploads at once: %v", name, err)
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("the %s upload never ended", name)
		}
	}
	restore()

	ctx, cancel := context.WithCancel(context.Background())
	chunk := 0
	restore = client.SetRequestStage(func(stage string) {
		if stage == "upload chunk" {
			chunk++
			if chunk == 2 {
				cancel()
			}
		}
	})
	if _, err := c.Upload(ctx, id, "c.txt", data('c')); !errors.Is(err, context.Canceled) {
		t.Errorf("an upload with a cancel after its first chunk = %v", err)
	}
	restore()
	for deadline := time.Now().Add(5 * time.Second); temps() != 1; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("expected 1 unfinished upload file before the next upload, got %d", temps())
		}
	}
	p, err := c.Upload(context.Background(), id, "d.txt", data('d'))
	if err == nil {
		err = same(p, data('d'))
	}
	if err != nil {
		t.Errorf("the upload after a cancelled one: %v", err)
	}
	if n := temps(); n != 0 {
		t.Errorf("the cancelled upload left %d files", n)
	}
}

// same says whether the file at p holds want.
func same(p string, want []byte) error {
	got, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("%s holds %d bytes that differ from the %d sent", p, len(got), len(want))
	}
	return nil
}
