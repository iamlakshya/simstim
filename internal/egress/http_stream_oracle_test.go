package egress

import (
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestHTTPProtocolSSEReachesTheGuestBeforeTheOriginFinishes(t *testing.T) {
	for _, guestProtocol := range []int{1, 2} {
		for _, originProtocol := range []int{1, 2} {
			t.Run(fmt.Sprintf("guest%d_origin%d", guestProtocol, originProtocol), func(t *testing.T) {
				release, firstFlushed, originEnded := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var releaseOnce sync.Once
				resume := func() { releaseOnce.Do(func() { close(release) }) }
				originResult := make(chan error, 1)
				const first = "event: token\ndata: first\n\n"
				const last = "event: token\ndata: final\n\n"
				h := newHTTPProtocolOracle(t, originProtocol == 2, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					defer close(originEnded)
					if err := protocolOracleRequestError(r, originProtocol, http.MethodGet, "/allowed/events"); err != nil {
						originResult <- err
						http.Error(w, "bad oracle request", 500)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					if _, err := io.WriteString(w, first); err != nil {
						originResult <- err
						return
					}
					if err := http.NewResponseController(w).Flush(); err != nil {
						originResult <- err
						return
					}
					close(firstFlushed)
					select {
					case <-release:
					case <-r.Context().Done():
						originResult <- r.Context().Err()
						return
					}
					_, err := io.WriteString(w, last)
					originResult <- err
				}))
				t.Cleanup(resume)
				response, err := h.client(t, guestProtocol).Get("https://api.example.com/allowed/%65vents?kept=yes")
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				if response.StatusCode != http.StatusOK || response.ProtoMajor != guestProtocol {
					t.Fatalf("SSE response = %s %d", response.Proto, response.StatusCode)
				}
				prefix := make([]byte, len(first))
				if _, err := io.ReadFull(response.Body, prefix); err != nil || string(prefix) != first {
					t.Fatalf("first SSE event = %q, %v", prefix, err)
				}
				select {
				case <-firstFlushed:
				case <-time.After(5 * time.Second):
					t.Fatal("guest bytes had no flushed origin event")
				}
				select {
				case <-originEnded:
					t.Fatal("origin completed before the test released the second event")
				default:
				}
				resume()
				tail, err := io.ReadAll(response.Body)
				if err != nil || string(tail) != last {
					t.Fatalf("SSE tail = %q, %v", tail, err)
				}
				if err := <-originResult; err != nil {
					t.Fatal(err)
				}
				if h.dials.Load() != 1 || h.lookups.Load() != 1 {
					t.Fatalf("SSE checked dials=%d lookups=%d", h.dials.Load(), h.lookups.Load())
				}
				requireProtocolOracleAudit(t, h, http.MethodGet, "/allowed/events", http.StatusOK)
			})
		}
	}
}
