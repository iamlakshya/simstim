package host

import (
	"io"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

// BridgeInput returns the bridge's input. With `--idle-exit <seconds>` in
// args, the bridge process exits when nothing arrives from the client for
// that long. The client asks for this on transports where a dropped
// connection does not end the bridge (E2B's envd), and pings meanwhile.
func BridgeInput(in io.Reader, args []string) io.Reader {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--idle-exit" {
			if n, err := strconv.Atoi(args[i+1]); err == nil && n > 0 {
				return WatchInput(in, time.Duration(n)*time.Second, func() {
					// Exit skips the bridge's own defers: end its debug first.
					stopBridgesDebug("idle")
					os.Exit(0)
				})
			}
		}
	}
	return in
}

// WatchInput returns a reader that calls onIdle once when no read of in has
// returned data for limit.
func WatchInput(in io.Reader, limit time.Duration, onIdle func()) io.Reader {
	w := &watched{in: in}
	w.last.Store(time.Now().UnixNano())
	go func() {
		t := time.NewTicker(min(limit/4, time.Second))
		defer t.Stop()
		for range t.C {
			if time.Since(time.Unix(0, w.last.Load())) > limit {
				onIdle()
				return
			}
		}
	}()
	return w
}

type watched struct {
	in   io.Reader
	last atomic.Int64
}

func (w *watched) Read(p []byte) (int, error) {
	n, err := w.in.Read(p)
	if n > 0 {
		w.last.Store(time.Now().UnixNano())
	}
	return n, err //nolint:wrapcheck // a Reader returns io.EOF itself: callers compare it with ==
}
