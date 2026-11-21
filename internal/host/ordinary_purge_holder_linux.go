//go:build linux

package host

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func (h *Holder) serveOrdinaryPurgeV2(c *hclient, line *StartupLineV1, hook bool) {
	h.serveOrdinaryControlV2(c, line, hook, true)
}

// The existing holder owns both the original creation and its restoration.
// The outside client receives a completion object, never restoration rights
// over a process identified by a number or over unrelated session children.
func (h *Holder) serveOrdinaryPurgeRequestsV2(line *StartupLineV1, state ordinaryStopStateV1, joined *os.File) {
	fd, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		return
	}
	restored := os.NewFile(uintptr(fd), "original-ordinary-restoration-completion")
	defer restored.Close()
	if err := sendOrdinaryPrivateFileV1(line, 1, state, restored); err != nil {
		return
	}
	ctx, joinPeer, err := watchOrdinaryPurgePeerV2(line)
	if err != nil {
		return
	}
	var pause *ownedMainPauseV2
	var cohort *ordinaryHolderCohortV3
	committed, completed := false, false
	defer func() {
		// The restoration receipt is last: the actual header reader is
		// already joined by its later defer, then the native peer watcher
		// and both decision descriptors physically close here.
		joinPeer()
		for cohort != nil {
			if err := cohort.Close(); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if completed {
			for completeOrdinaryRestorationV2(restored) != nil {
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()
	// This producer may finish only by its actual restoration or physical
	// join. A global COMMIT remains authoritative after parent/stream loss.
	finish := func() {
		for cohort != nil {
			first, err := cohort.abortV3()
			if err != nil || first == purgeCohortPendingV3 {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			if first == purgeCohortCommitV3 {
				if pause == nil || pause.CheckPaused(context.Background()) != nil {
					time.Sleep(10 * time.Millisecond)
					continue
				}
				committed = true
				for ordinaryJoinLatchV1(joined, true) != nil {
					h.commitOrdinaryStopV1(line, 0, state, joined)
					if ordinaryJoinLatchV1(joined, true) != nil {
						time.Sleep(10 * time.Millisecond)
					}
				}
				return
			}
			if first == purgeCohortAbortV3 {
				break
			}
		}
		for pause != nil {
			if err := pause.Restore(context.Background()); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		completed = true
	}
	defer func() {
		if !committed && !completed {
			finish()
		}
	}()
	_ = line.SetDeadline(time.Time{})
	type nextRequest struct {
		state     ordinaryStopStateV1
		operation string
		err       error
	}
	requests := make(chan nextRequest, 1)
	continueRead := make(chan struct{})
	stopRead := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for id := uint64(2); id != 0; id++ {
			actual, operation, err := h.readOrdinaryControlRequestV2(line, id)
			select {
			case requests <- nextRequest{actual, operation, err}:
			case <-stopRead:
				return
			}
			if err != nil {
				return
			}
			// Admission reads its two actual native loans on this same line
			// while the header reader is physically held here.
			select {
			case <-continueRead:
			case <-stopRead:
				return
			}
		}
	}()
	defer func() { close(stopRead); _ = line.conn.Close(); <-readerDone }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for id := uint64(2); id != 0; {
		if cohort != nil {
			first, err := cohort.firstV3()
			if err != nil {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			if first != purgeCohortPendingV3 {
				finish()
				return
			}
		}
		var request nextRequest
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			continue
		case request = <-requests:
		}
		if request.err != nil || request.state != state || ctx.Err() != nil {
			return
		}
		switch request.operation {
		case ordinaryPurgeBindCohortV3:
			if cohort != nil || pause != nil || ownUnit(h.id) != "" {
				return
			}
			h.mu.Lock()
			managed := h.m.Run.Unit != ""
			h.mu.Unlock()
			if managed {
				return
			}
			cohort, err = receiveOrdinaryHolderCohortV3(line, id, state)
			if err != nil {
				return
			}
		case ordinaryPurgeCheckV2:
			if err := h.ordinary.native.check(ctx); err != nil {
				return
			}
			if pause != nil && pause.CheckPaused(ctx) != nil {
				return
			}
		case ordinaryPurgeQuiesceV2:
			if pause != nil || ownUnit(h.id) != "" {
				return
			}
			h.mu.Lock()
			managed := h.m.Run.Unit != ""
			h.mu.Unlock()
			if managed {
				return
			}
			pause, err = h.quiesceOwnedMainV2(ctx)
			if err != nil {
				return
			}
		case ordinaryPurgeCheckQuiescedV2:
			if pause == nil || pause.CheckPaused(ctx) != nil {
				return
			}
		case ordinaryPurgeRestoreV2:
			finish()
			if completed {
				_ = h.writeOrdinaryStateV1(line, id, state)
			}
			return
		case ordinaryStopCommitV1:
			if cohort != nil {
				// Bound monitors accept only the first native cohort packet.
				// A stream commit cannot choose or arm a local decision.
				return
			}
			if pause == nil || pause.CheckPaused(ctx) != nil {
				return
			}
			committed = true
			h.commitOrdinaryStopV1(line, id, state, joined)
			return
		default:
			return
		}
		if err := h.writeOrdinaryStateV1(line, id, state); err != nil {
			return
		}
		id++
		select {
		case continueRead <- struct{}{}:
		case <-ctx.Done():
			return
		}
	}
}

func completeOrdinaryRestorationV2(restored *os.File) error {
	var completion [8]byte
	binary.NativeEndian.PutUint64(completion[:], 1)
	n, err := restored.Write(completion[:])
	if err == nil && n != len(completion) {
		return sessionLeaseError("the original ordinary restoration has no complete native acknowledgement")
	}
	return err
}

// Duplicate the original authenticated peer handle before the first pause.
// Its native death cancels only this handler and joins its existing restore.
func watchOrdinaryPurgePeerV2(line *StartupLineV1) (context.Context, func(), error) {
	peer := line.nativePeer
	if peer == nil {
		return nil, nil, sessionLeaseError("the reversible ordinary operation has no original outside peer")
	}
	peer.mu.Lock()
	if err := peer.checkLocked(); err != nil {
		peer.mu.Unlock()
		return nil, nil, err
	}
	fd, err := unix.FcntlInt(uintptr(peer.pidfd), unix.F_DUPFD_CLOEXEC, 0)
	peer.mu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer unix.Close(fd)
		for ctx.Err() == nil {
			polls := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			n, err := unix.Poll(polls, 100)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil || n != 0 {
				cancel()
				_ = line.conn.Close()
				return
			}
		}
	}()
	return ctx, func() { cancel(); <-done }, nil
}
