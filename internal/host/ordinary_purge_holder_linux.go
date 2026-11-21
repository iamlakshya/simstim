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
	defer joinPeer()
	var pause *ownedMainPauseV2
	committed, completed := false, false
	defer func() {
		if committed || completed {
			return
		}
		// EOF, cancellation and original-parent death cancel this existing
		// reversible operation. They supply no signal or cleanup authority.
		for pause != nil {
			if err := pause.Restore(context.Background()); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		_ = completeOrdinaryRestorationV2(restored)
	}()
	_ = line.SetDeadline(time.Time{})
	for id := uint64(2); id != 0; id++ {
		actual, operation, err := h.readOrdinaryControlRequestV2(line, id)
		if err != nil || actual != state || ctx.Err() != nil {
			return
		}
		switch operation {
		case ordinaryPurgeCheckV2:
			if err := h.ordinary.native.check(ctx); err != nil {
				return
			}
			if pause != nil && pause.CheckPaused(ctx) != nil {
				return
			}
		case ordinaryPurgeQuiesceV2:
			// A managed holder freezes with its unit. Its distinct outside
			// restorer must be joined before whole-unit freeze is enabled.
			// This bounded producer never presents a main pause as that claim.
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
			if pause != nil && pause.Restore(ctx) != nil {
				return
			}
			if completeOrdinaryRestorationV2(restored) != nil {
				return
			}
			completed = true
			_ = h.writeOrdinaryStateV1(line, id, state)
			return
		case ordinaryStopCommitV1:
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
