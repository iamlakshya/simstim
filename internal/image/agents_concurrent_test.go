//go:build (linux || darwin) && (amd64 || arm64)

package image

import (
	"context"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestAgentsConcurrentSameSourcePublishersReturnSharedLeases(t *testing.T) {
	imageDomainHome(t)
	store := imageDomainStore(t, imageDomainFixture("a", "b"))
	build, _ := agentsStoreBuilder(t, "same installed executable")
	ready, proceed := make(chan struct{}, 2), make(chan struct{})
	barrier := func(ctx context.Context, previous string, output *os.File) (AgentsBuildResultV1, error) {
		result, err := build(ctx, previous, output)
		ready <- struct{}{}
		select {
		case <-proceed:
			return result, err
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	type outcome struct {
		disk  AgentDiskV1
		lease *Lease
		err   error
	}
	done := make(chan outcome, 2)
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			disk, lease, err := store.EnsureAgentsV1(ctx, runtime.GOARCH, barrier)
			done <- outcome{disk, lease, err}
		}()
	}
	for range 2 {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal("builders did not run without a native store lock", ctx.Err())
		}
	}
	close(proceed)
	var digest string
	for range 2 {
		select {
		case result := <-done:
			if result.err != nil {
				t.Fatal(result.err)
			}
			defer result.lease.Close() // first stays held while second returns
			if digest != "" && digest != result.disk.Content.SHA256 {
				t.Fatal("same source produced different disks")
			}
			digest = result.disk.Content.SHA256
		case <-ctx.Done():
			t.Fatal("a raced writer waited on another returned retention lease", ctx.Err())
		}
	}
}

func TestAgentsOlderConcurrentSnapshotCannotReplaceDifferentNewerSource(t *testing.T) {
	imageDomainHome(t)
	store := imageDomainStore(t, imageDomainFixture("a", "b"))
	oldBuild, _ := agentsStoreBuilder(t, "old executable")
	newBuild, _ := agentsStoreBuilder(t, "new executable")
	ready, proceed := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	var worker sync.WaitGroup
	worker.Add(1)
	defer func() { cancel(); worker.Wait() }()
	go func() {
		defer worker.Done()
		_, lease, err := store.EnsureAgentsV1(ctx, runtime.GOARCH, func(ctx context.Context, previous string, output *os.File) (AgentsBuildResultV1, error) {
			result, err := oldBuild(ctx, previous, output)
			close(ready)
			select {
			case <-proceed:
				return result, err
			case <-ctx.Done():
				return result, ctx.Err()
			}
		})
		if lease != nil {
			lease.Close()
		}
		done <- err
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	newest, lease, err := store.EnsureAgentsV1(ctx, runtime.GOARCH, newBuild)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	close(proceed)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("old snapshot overwrote another completed source")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	root, err := store.agentsRoot(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	current, err := store.readAgentsSource(root)
	if err != nil || current.Disk.SourceDigest != newest.SourceDigest || current.Disk.Content != newest.Content {
		t.Fatal("newest pointer was rewound", current, err)
	}
}
