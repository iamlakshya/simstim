package egress

import (
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestHTTPSecretRouteRevocationClosesTheOriginWhileGzipHeaderIsIncomplete(t *testing.T) {
	started := make(chan struct{}, 1)
	originDone := make(chan struct{})
	f := newSecretRouteFixture(t, 2, 2, secretRouteArgs(t, secretRoutePolicy(t)), http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		defer close(originDone)
		if req.Header.Get("Authorization") != "Bearer "+secretRouteReal {
			http.Error(w, "wrong current credential", 400)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		if http.NewResponseController(w).Flush() != nil {
			return
		}
		if _, err := w.Write([]byte{0x1f}); err != nil {
			return
		}
		if http.NewResponseController(w).Flush() != nil {
			return
		}
		started <- struct{}{}
		<-req.Context().Done()
	}))
	req, err := http.NewRequestWithContext(f.ctx, http.MethodGet, "https://"+secretRouteAuthority+"/allowed/gzip", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.sentinel())
	finished := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		defer close(finished)
		response, err := f.client.Do(req)
		if err != nil {
			result <- nil
			return
		}
		body, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if strings.Contains(string(body), secretRouteReal) {
			result <- errors.New("cancelled gzip route exposed a real value")
			return
		}
		if readErr == nil && response.StatusCode == http.StatusOK {
			result <- errors.New("incomplete gzip response appeared complete")
			return
		}
		result <- nil
	}()
	t.Cleanup(func() {
		f.cancel()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("owned incomplete-gzip request did not join")
		}
	})
	waitHTTPProgress(t, started, "the actual incomplete gzip origin header")
	f.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: secretRoutePolicy(t, api.Rule{Allow: true, Target: secretRouteAuthority + "/allowed/other"})}, nil)
	waitHTTPState(t, func() bool { return f.originCloses.Load() == 1 }, "the actual owned origin socket close")
	waitHTTPProgress(t, originDone, "the actual gzip origin handler end")
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("revoked gzip decoder left the guest request alive")
	}
	if f.lookups.Load() != 1 || f.dials.Load() != 1 || f.requests.Load() != 1 {
		t.Fatal("gzip revocation retried or lost its checked origin owner")
	}
}

type secretRouteCloseConn struct {
	net.Conn
	closed atomic.Bool
}

func (c *secretRouteCloseConn) Close() error { c.closed.Store(true); return c.Conn.Close() }

type secretRouteDecoderClose struct {
	conn  *secretRouteCloseConn
	calls int
}

func (*secretRouteDecoderClose) Read([]byte) (int, error) { return 0, io.EOF }
func (r *secretRouteDecoderClose) Close() error {
	r.calls++
	if !r.conn.closed.Load() {
		return errors.New("decoder closed before its actual owned origin socket")
	}
	return nil
}

// This narrow production-adapter oracle checks the close ordering that is
// not observable from the black-box gzip route above. It adds no test hook.
func TestHTTPSecretOriginAdapterClosesOwnedSocketBeforeDecoder(t *testing.T) {
	client, peer := net.Pipe()
	defer peer.Close()
	conn := &secretRouteCloseConn{Conn: client}
	defer conn.Close()
	decoder := &secretRouteDecoderClose{conn: conn}
	scope := &originRequest{conn: conn}
	input := &secretOriginInput{secretOriginInputState: &secretOriginInputState{reader: decoder, scope: scope}}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	if err := input.Close(); err != nil || decoder.calls != 1 {
		t.Fatal("repeated owned close re-entered the decoder")
	}
}
