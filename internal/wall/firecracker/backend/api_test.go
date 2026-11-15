package backend

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

func pipeControl(t *testing.T, serve func(net.Conn)) *Client {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	control, err := NewClient(client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		serve(server)
	}()
	t.Cleanup(func() {
		client.Close()
		server.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the owned pipe server did not stop")
		}
	})
	return control
}

func replyControl(conn net.Conn, status int, body string) error {
	response := http.Response{
		StatusCode:    status,
		ProtoMajor:    1,
		ProtoMinor:    1,
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Header:        make(http.Header),
	}
	return response.Write(conn)
}

func TestPrivateControlUsesSameConnection(t *testing.T) {
	errCh := make(chan error, 1)
	control := pipeControl(t, func(conn net.Conn) {
		reader := bufio.NewReader(conn)
		for i, want := range []struct{ method, path, body string }{
			{http.MethodGet, "/", ""},
			{http.MethodPut, "/actions", `{"action_type":"SendCtrlAltDel"}`},
		} {
			request, err := http.ReadRequest(reader)
			if err != nil {
				errCh <- err
				return
			}
			body, err := io.ReadAll(request.Body)
			request.Body.Close()
			if err != nil || request.Method != want.method || request.URL.Path != want.path || string(body) != want.body {
				errCh <- fmt.Errorf("request %d: method=%q path=%q body=%q error=%v", i, request.Method, request.URL.Path, body, err)
				return
			}
			status, result := http.StatusOK, `{"state":"Running"}`
			if i == 1 {
				status, result = http.StatusNoContent, ""
				if request.Header.Get("Content-Type") != "application/json" {
					errCh <- fmt.Errorf("missing action content type")
					return
				}
			}
			if err := replyControl(conn, status, result); err != nil {
				errCh <- err
				return
			}
		}
		errCh <- nil
	})
	state, err := control.State(t.Context())
	if err != nil || state != "Running" {
		t.Fatalf("state = %q, %v", state, err)
	}
	if err := control.PowerButton(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}

func TestPrivateControlStateIsClosedVocabulary(t *testing.T) {
	for _, state := range []string{"Not started", "Running", "Paused", "", "Stopped", "running"} {
		t.Run(state, func(t *testing.T) {
			control := pipeControl(t, func(conn net.Conn) {
				if _, err := http.ReadRequest(bufio.NewReader(conn)); err != nil {
					return
				}
				data, _ := json.Marshal(map[string]string{"state": state})
				_ = replyControl(conn, http.StatusOK, string(data))
			})
			got, err := control.State(t.Context())
			valid := state == "Not started" || state == "Running" || state == "Paused"
			if valid && (err != nil || got != state) || !valid && err == nil {
				t.Fatalf("state = %q, %v for %q", got, err, state)
			}
		})
	}
}

func TestPrivateControlRejectsMalformedOrOversizeReply(t *testing.T) {
	for _, body := range []string{`{"state":"Running"} {}`, `{"state":1}`, `null`, strings.Repeat("x", proto.MaxFrame+1)} {
		t.Run(fmt.Sprintf("bytes_%d", len(body)), func(t *testing.T) {
			control := pipeControl(t, func(conn net.Conn) {
				if _, err := http.ReadRequest(bufio.NewReader(conn)); err != nil {
					return
				}
				_ = replyControl(conn, http.StatusOK, body)
			})
			if _, err := control.State(t.Context()); err == nil {
				t.Fatal("accepted a malformed or oversized state response")
			}
		})
	}
}

func TestPrivateControlNeverFollowsRedirectOrRedials(t *testing.T) {
	control := pipeControl(t, func(conn net.Conn) {
		if _, err := http.ReadRequest(bufio.NewReader(conn)); err != nil {
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 302 Found\r\nContent-Length: 0\r\nConnection: close\r\nLocation: http://other.invalid/\r\n\r\n")
	})
	if _, err := control.State(t.Context()); err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("redirect error = %v", err)
	}
	if _, err := control.State(t.Context()); err == nil {
		t.Fatal("reopened the runner's closed control connection")
	}
}

func TestPrivateControlCancellationAndClose(t *testing.T) {
	read := make(chan struct{})
	control := pipeControl(t, func(conn net.Conn) {
		if _, err := http.ReadRequest(bufio.NewReader(conn)); err != nil {
			return
		}
		close(read)
		_, _ = io.Copy(io.Discard, conn)
	})
	firstCtx, cancelFirst := context.WithCancel(t.Context())
	firstDone := make(chan error, 1)
	go func() { _, err := control.State(firstCtx); firstDone <- err }()
	<-read
	queuedCtx, cancelQueued := context.WithCancel(t.Context())
	cancelQueued()
	if _, err := control.State(queuedCtx); err != context.Canceled {
		t.Fatalf("queued cancellation = %v", err)
	}
	cancelFirst()
	select {
	case err := <-firstDone:
		if err == nil {
			t.Fatal("cancelled in-flight call succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not end the in-flight call")
	}
	if err := control.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := control.State(t.Context()); err != net.ErrClosed {
		t.Fatalf("state after Close = %v", err)
	}
	if err := control.Close(); err != nil {
		t.Fatal(err)
	}
}
