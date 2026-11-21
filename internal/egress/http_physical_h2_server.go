package egress

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"golang.org/x/net/http2"
)

func (*physicalH2Conn) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

func (c *physicalH2Conn) Close() error {
	c.closeOnce.Do(func() {
		c.owner.mu.Lock()
		c.owner.closed = true
		var cancel []context.CancelFunc
		for _, stream := range c.owner.streams {
			if stream.cancel != nil {
				cancel = append(cancel, stream.cancel)
			}
		}
		c.owner.notifyLocked()
		c.owner.mu.Unlock()
		// Stop actual native IO before filter/request cancellation. No join
		// runs recursively inside a writer or a request finalisation callback.
		c.closeErr = c.actual.NetConn().Close()
		c.cancel()
		for _, stop := range cancel {
			stop()
		}
	})
	return c.closeErr
}

// Called by the external connection owner after Close, never from its writer.
// Outstanding native-write callbacks finish before incomplete settlement.
func (c *physicalH2Conn) joinPhysicalH2() {
	_ = c.Close()
	c.writes.Wait()
	c.owner.handlers.Wait()
	c.readMu.Lock()
	clear(c.readPending)
	c.readPending = nil
	c.inRaw.Reset()
	c.controlMu.Lock()
	clear(c.controls)
	c.controls = nil
	c.credit = 0
	c.controlMu.Unlock()
	c.readMu.Unlock()
	c.owner.mu.Lock()
	var finish []func()
	for _, stream := range c.owner.streams {
		// Close cut the original native socket, and every admitted Write
		// has now returned. Selected frames which could not finish no
		// longer retain an outstanding native writer.
		stream.selected = 0
		if !stream.settled {
			stream.reset, stream.settled = true, true
		}
		if callback := c.owner.finishLocked(stream); callback != nil {
			finish = append(finish, callback)
		}
	}
	clear(c.owner.fences)
	clear(c.owner.guestPings)
	clear(c.owner.key.secret[:])
	c.owner.mu.Unlock()
	for _, callback := range finish {
		go callback()
	}
	c.owner.callbacks.Wait()
}

// Serve the same inherited, established TLS stream through the pinned public
// HTTP2 server and its original native writer. No listening socket is opened.
func servePhysicalHTTP2(ctx context.Context, actual *tls.Conn, handler http.Handler) error {
	if ctx == nil || actual == nil || handler == nil {
		return errPhysicalH2Binding
	}
	if err := actual.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		return err
	}
	if err := actual.HandshakeContext(ctx); err != nil {
		return err
	}
	if err := actual.SetDeadline(time.Time{}); err != nil {
		return err
	}
	conn, err := newPhysicalH2Conn(ctx, actual)
	if err != nil {
		return err
	}
	defer conn.joinPhysicalH2()
	conn.owner.scheduler = true
	protocols := new(http.Protocols)
	protocols.SetHTTP2(true)
	server := &http.Server{
		Protocols:         protocols,
		ReadHeaderTimeout: handshakeTimeout,
		MaxHeaderBytes:    maxHTTPHeaderBytes + physicalH2TokenOverhead,
		HTTP2:             &http.HTTP2Config{MaxConcurrentStreams: maxActiveFlows, MaxReadFrameSize: physicalH2OriginalFrameLimit, MaxDecoderHeaderTableSize: physicalH2OriginalTableLimit},
		Handler:           conn.deliveryHandler(handler),
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	// The pinned public scheduler API supplies the original queue-purge
	// callback; the standard server exposes no equivalent hook. The module
	// remains pinned and uses the same original HTTP2 settings above.
	h2 := &http2.Server{NewWriteScheduler: func() http2.WriteScheduler {
		return &physicalH2Scheduler{WriteScheduler: http2.NewRandomWriteScheduler(), owner: conn.owner}
	}}
	h2.ServeConn(&physicalH2NativeTLSView{Conn: conn, owner: conn.owner}, &http2.ServeConnOpts{Context: ctx, BaseConfig: server, Handler: server.Handler})
	_ = conn.Close()
	return nil
}

// The existing request owner records its final evidence before returning.
// This does not settle transport IO or finish a meter turn by itself.
func (s *physicalH2Delivery) completionEvidence(complete bool) {
	s.owner.mu.Lock()
	s.complete = complete
	s.owner.mu.Unlock()
}

// The sealed native proving fixture uses the production dispatcher as well.
func servePhysicalHTTP2Prototype(ctx context.Context, actual *tls.Conn, handler http.Handler) error {
	return servePhysicalHTTP2(ctx, actual, handler)
}

func (conn *physicalH2Conn) deliveryHandler(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		conn.owner.mu.Lock()
		if conn.owner.closed {
			conn.owner.mu.Unlock()
			http.Error(w, errPhysicalH2Binding.Error(), http.StatusServiceUnavailable)
			return
		}
		conn.owner.handlers.Add(1)
		conn.owner.mu.Unlock()
		defer conn.owner.handlers.Done()
		request, stream, err := conn.owner.claim(request)
		if err != nil {
			http.Error(w, errPhysicalH2Binding.Error(), http.StatusBadRequest)
			return
		}
		defer func() {
			conn.owner.mu.Lock()
			complete := stream.complete
			conn.owner.mu.Unlock()
			stream.handlerFinished(complete)
		}()
		conn.owner.mu.Lock()
		truncated := stream.truncated
		conn.owner.mu.Unlock()
		if truncated {
			http.Error(w, "request headers exceed the original limit. Try: send fewer headers", http.StatusRequestHeaderFieldsTooLarge)
			return
		}
		request.RemoteAddr = conn.actual.RemoteAddr().String()
		handler.ServeHTTP(w, request)
	})
}
