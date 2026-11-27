package egress

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// Main calls this before track or any cancellation callback can publish this
// request as an owner. A guest H2 write must be interruptible independently of
// the shared local TLS carrier and every other guest stream.
func (r *originRequest) bindResponseWriter(w http.ResponseWriter) error {
	if !r.http2 {
		return nil
	}
	controller := http.NewResponseController(w)
	if controller.SetWriteDeadline(time.Time{}) != nil {
		return memberError("secret_lifetime", "request has no independently interruptible response writer", "Retry with matching HTTP lifetime support.")
	}
	r.writerMu.Lock()
	defer r.writerMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.ctx.Err() != nil {
		return memberError("cancelled", "request ended before response writer admission", "Retry the request after the approved operation finishes.")
	}
	r.writer = controller
	return nil
}

// releaseResponseWriter waits for an actual deadline operation and removes
// the handler-owned writer before net/http may recycle it. Later aborts still
// close their own origin, but cannot touch a completed handler's writer.
func (r *originRequest) releaseResponseWriter() {
	r.writerMu.Lock()
	r.writer = nil
	r.writerMu.Unlock()
}

func (r *originRequest) watchClientCancellation(ctx context.Context) func() {
	finished := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(finished)
		r.abort()
	})
	return sync.OnceFunc(func() {
		if !stop() {
			<-finished
		}
	})
}
