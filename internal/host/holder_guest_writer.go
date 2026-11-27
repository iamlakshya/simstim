package host

import (
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

// guestClaimsDrain bounds starting further ordinary claims after an end.
// A partial frame still finishes first, within the holder's final flush.
const guestClaimsDrain = time.Second

type guestWrite uint8

const (
	guestWritten guestWrite = iota
	guestSkipped
	guestClosed
)

// finishEvent drains a bounded snapshot of prior claims, preserving a recent
// agent ID on a healthy socket. An unsent claim that times out ends the drain.
func (g *guestEvents) finishEvent(ev proto.CtlEvent) {
	for range len(g.out) {
		select {
		case prev := <-g.out:
			switch g.writeEvent(prev, true) {
			case guestWritten:
			case guestClosed:
				return
			case guestSkipped:
				g.dropClaims()
				g.writeEvent(ev, false)
				return
			}
		default:
		}
	}
	g.writeEvent(ev, false)
}

func (g *guestEvents) dropClaims() {
	for range len(g.out) {
		select {
		case <-g.out:
		default:
		}
	}
}

// writeEvent may skip only a wholly unsent ordinary claim. A partial frame
// always finishes before a different frame begins, so the stream stays valid.
func (g *guestEvents) writeEvent(ev proto.CtlEvent, ordinary bool) guestWrite {
	payload, err := json.Marshal(ev)
	if err != nil {
		return guestSkipped
	}
	raw, err := proto.EncodeFrame(proto.TEvent, payload)
	if err != nil {
		return guestSkipped
	}
	wrote := 0
	for len(raw) > 0 {
		deadline, skip := g.eventWriteDeadline(ordinary && wrote == 0)
		if skip {
			return guestSkipped
		}
		_ = g.conn.SetWriteDeadline(deadline)
		n, werr := g.conn.Write(raw)
		wrote += n
		raw = raw[n:]
		if werr == nil {
			continue
		}
		if !errors.Is(werr, os.ErrDeadlineExceeded) {
			return guestClosed
		}
		if ordinary && wrote == 0 && g.endQueued.Load() {
			return guestSkipped
		}
	}
	_ = g.conn.SetWriteDeadline(time.Time{})
	return guestWritten
}

func (g *guestEvents) eventWriteDeadline(unsent bool) (time.Time, bool) {
	now := time.Now()
	deadline := now.Add(time.Second)
	if unsent && g.endQueued.Load() {
		g.mu.Lock()
		endBy := g.endBy
		g.mu.Unlock()
		if !now.Before(endBy) {
			return endBy, true
		}
		if endBy.Before(deadline) {
			deadline = endBy
		}
	}
	return deadline, false
}
