package egress

import (
	"io"
	"net/http"
	"testing"
	"time"
)

func TestHTTPProtocolGuestDisconnectEndsAnIdleStreamingOrigin(t *testing.T) {
	stopped := make(chan struct{})
	h := newHTTPProtocolOracle(t, true, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: ready\n\n")
		if err := http.NewResponseController(w).Flush(); err != nil {
			close(stopped)
			return
		}
		<-req.Context().Done()
		close(stopped)
	}))
	response, err := h.client(t, 1).Get("https://api.example.com/allowed/events?kept=yes")
	if err != nil {
		t.Fatal(err)
	}
	first := make([]byte, len("data: ready\n\n"))
	if _, err := io.ReadFull(response.Body, first); err != nil {
		_ = response.Body.Close()
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	// The origin sends no further event. EOF observation, rather than a
	// later failed response write, must cancel its real request context.
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("guest disconnect retained an idle external streaming request")
	}
}
