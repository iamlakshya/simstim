package guest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestImageChangesPagesOwnSnapshotAndFitActualCtlBound(t *testing.T) {
	ready := make(chan struct{})
	close(ready)
	operation := &imageSaveGuestV1{ctx: context.Background(), ready: ready}
	for index := 0; index < 512; index++ {
		operation.paths = append(operation.paths, fmt.Sprintf("/changed/%04d-%s", index, strings.Repeat("quote\"", 70)))
	}
	offset, pages := 0, 0
	for {
		result, err := operation.page(strings.Repeat("a", 32), offset)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(result)
		if err != nil || len(data) > proto.ImageChangesPageBytes {
			t.Fatal("page exceeded actual control bound", len(data), err)
		}
		if result.Next <= offset && !result.Done {
			t.Fatal("page made no progress")
		}
		if len(result.Paths) > 0 {
			before := operation.paths[offset]
			result.Paths[0] = "/caller/mutation"
			if operation.paths[offset] != before {
				t.Fatal("page aliases original changes snapshot")
			}
		}
		offset = result.Next
		pages++
		if result.Done {
			break
		}
	}
	if offset != len(operation.paths) || pages < 2 {
		t.Fatal("paged changes lost original paths", offset, pages)
	}
	if _, err := operation.page(strings.Repeat("a", 32), len(operation.paths)+1); err == nil {
		t.Fatal("page accepted offset outside original reservation")
	}
}

func TestImageExportCancelJoinsWriterBeforeClosingOriginalRoots(t *testing.T) {
	// Explicit lifecycle fixture; these temporary roots mint no native VM grant.
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	upper, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	defer upper.Close()
	ready := make(chan struct{})
	close(ready)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(done) }) }
	t.Cleanup(unblock)
	var released atomic.Int32
	operation := &imageSaveGuestV1{root: root, upper: upper, ctx: ctx, cancel: cancel, ready: ready, writeDone: done, release: func() { released.Add(1) }}
	id := strings.Repeat("a", 32)
	other := &imageSaveGuestV1{}
	guest := &Init{imageSaves: map[string]*imageSaveGuestV1{id: operation, "foreign": other}}
	joined := make(chan error, 1)
	go func() { joined <- guest.cancelImageSaveGuestV1(id) }()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not reach original writer")
	}
	if _, err := root.Stat("."); err != nil || released.Load() != 0 {
		t.Fatal("root/receipt closed before writer joined", err)
	}
	unblock()
	select {
	case err := <-joined:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not join original writer")
	}
	if _, err := root.Stat("."); err == nil {
		t.Fatal("owned root remains open after cancel joined")
	}
	if released.Load() != 1 || guest.imageSaves[id] != nil || guest.imageSaves["foreign"] != other {
		t.Fatal("cancel lost exact resource/receipt ownership")
	}
	if err := guest.cancelImageSaveGuestV1(id); err != nil || released.Load() != 1 {
		t.Fatal("duplicate cancel released another receipt", err)
	}
}
