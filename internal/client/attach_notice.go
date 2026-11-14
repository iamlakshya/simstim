package client

import "context"

// maxBackground is how many notices about other sessions wait at most for
// the terminal. The others are dropped.
const maxBackground = 8

// heldNotice is a notice that waits for the terminal.
type heldNotice struct {
	seq  []byte
	keep bool // a notice of the user's own action, which is never dropped
}

// notice writes a notice when the terminal is between sequences and
// characters. Otherwise it holds the notice until the session's output gets
// there, so the notice never cuts a sequence of the session's.
func (a *attacher) notice(ctx context.Context, seq []byte, keep bool) {
	if a.outStream.ground() && a.guard.idle() {
		a.write(ctx, seq)
		return
	}
	if !keep {
		n := 0
		for _, h := range a.held {
			if !h.keep {
				n++
			}
		}
		if n >= maxBackground {
			return
		}
	}
	a.held = append(a.held, heldNotice{seq: seq, keep: keep})
}

// writeHeld writes the notices that waited, once the terminal is between
// sequences.
func (a *attacher) writeHeld(ctx context.Context) {
	if len(a.held) == 0 || !a.outStream.ground() || !a.guard.idle() {
		return
	}
	held := a.held
	a.held = nil
	for _, n := range held {
		a.write(ctx, n.seq)
	}
}

// endGuard runs at detach, after the reset of the session's modes. It ends
// an iTerm2 copy that the session left running, which records it. A kitty
// transfer that has not ended counts as a write as far as it went, and its
// state waits for the next attach in this process. Then it writes the
// notices that still wait, and waits for the clipboard writes to reach the
// shared file.
func (a *attacher) endGuard(ctx context.Context) {
	if a.guard == nil {
		return
	}
	if a.guard.copying() {
		a.write(ctx, []byte("\x1b]1337;EndCopy\a"))
	}
	a.guard.detach(ctx)
	for _, n := range a.held {
		a.write(ctx, n.seq)
	}
	a.held = nil
	a.guard.mem.flush(ctx, guardFlushWait)
}
