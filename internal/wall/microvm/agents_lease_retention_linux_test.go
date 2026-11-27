//go:build linux

package microvm

import (
	"errors"
	"os"
	"sync"
	"testing"
)

// This controlled close failure tests resource retention only. It supplies no
// native phase, Source, child, capacity or physical-exit acknowledgement.
type pendingFileCloseV1 struct {
	file  *os.File
	ready bool
	calls int
}

func (close *pendingFileCloseV1) Close() error {
	close.calls++
	if !close.ready {
		return errors.New("original resource closure pending")
	}
	return close.file.Close()
}

func TestAgentsLeaseRetainsActualDescriptorUntilSuccessfulRetryV1(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "actual-retained-resource-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	closer := &pendingFileCloseV1{file: file}
	s := &sandbox{agentsLease: closer}
	if err := s.releaseAgentsLease(); err == nil || s.agentsLease != closer {
		t.Fatal("failed close discarded the original resource owner", err)
	}
	if _, err := file.Stat(); err != nil {
		t.Fatal("pending close released the actual descriptor", err)
	}
	closer.ready = true
	if err := s.releaseAgentsLease(); err != nil || s.agentsLease != nil || closer.calls != 2 {
		t.Fatal("successful retry did not release the same original owner", err, closer.calls)
	}
	if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("successful retry left the real descriptor open", err)
	}
}

type joinedFileCloseV1 struct {
	file    *os.File
	entered chan struct{}
	release chan struct{}
	calls   int
}

func (close *joinedFileCloseV1) Close() error {
	close.calls++
	close.entered <- struct{}{}
	<-close.release
	return close.file.Close()
}

func TestAgentsLeaseConcurrentReleaseJoinsOneActualCloseV1(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "actual-joined-resource-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	closer := &joinedFileCloseV1{file: file, entered: make(chan struct{}, 2), release: make(chan struct{})}
	s := &sandbox{agentsLease: closer}
	var workers sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		workers.Add(1)
		go func() { defer workers.Done(); results <- s.releaseAgentsLease() }()
	}
	<-closer.entered
	close(closer.release)
	workers.Wait()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if closer.calls != 1 || s.agentsLease != nil {
		t.Fatal("concurrent cleanup closed the same actual owner more than once", closer.calls)
	}
}
