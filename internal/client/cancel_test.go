package client

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Every request to a host goes through Call, and none goes after its
// context has ended: Call and Upload return the cancel, and write nothing.
func TestNoRequestAfterACancel(t *testing.T) {
	var sent bytes.Buffer
	c := &Conn{w: proto.NewWriter(&sent), pending: map[uint64]chan proto.Response{}, done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Call(ctx, "doctor.fix", api.FixArgs{Apply: true}, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("Call after a cancel = %v", err)
	}
	if _, err := c.Upload(ctx, "s1", "a.png", []byte("x")); !errors.Is(err, context.Canceled) {
		t.Errorf("Upload after a cancel = %v", err)
	}
	if sent.Len() != 0 {
		t.Errorf("the client sent %q after the cancel", sent.Bytes())
	}
	if len(c.pending) != 0 {
		t.Errorf("%d requests wait for an answer", len(c.pending))
	}
}

// stuckWriter is a connection to a host whose first write waits until the
// test lets it go, and that keeps what it got.
type stuckWriter struct {
	mu      sync.Mutex
	got     bytes.Buffer
	writes  int
	entered chan struct{}
	release chan struct{}
}

func (s *stuckWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.writes++
	first := s.writes == 1
	s.mu.Unlock()
	if first {
		s.entered <- struct{}{}
		<-s.release
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.got.Write(p)
}

// A request that waits for its turn behind another frame, and is
// cancelled while it waits, ends at once and never reaches the host, also
// once the other frame is out: a call, and an upload.
func TestARequestCancelledInTheQueueSendsNothing(t *testing.T) {
	for _, c := range []struct {
		name, stage string
		send        func(ctx context.Context, c *Conn) error
	}{
		{"a call", "queued", func(ctx context.Context, c *Conn) error {
			return c.Call(ctx, "doctor.fix", api.FixArgs{Apply: true}, nil)
		}},
		{"an upload", "upload begin", func(ctx context.Context, c *Conn) error {
			_, err := c.Upload(ctx, "s1", "a.png", []byte("x"))
			return err
		}},
	} {
		out := &stuckWriter{entered: make(chan struct{}, 1), release: make(chan struct{})}
		conn := &Conn{w: proto.NewWriter(out), pending: map[uint64]chan proto.Response{}, done: make(chan struct{})}
		first := make(chan error, 1)
		go func() { first <- conn.w.Write(proto.TPing, nil) }()
		<-out.entered // the ping holds the writer
		ctx, cancel := context.WithCancel(context.Background())
		queued := make(chan struct{})
		old := requestStage
		requestStage = func(stage string) {
			if stage == c.stage {
				close(queued)
			}
		}
		done := make(chan error, 1)
		go func() { done <- c.send(ctx, conn) }()
		<-queued
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("%s cancelled in the queue = %v", c.name, err)
			}
		case <-time.After(5 * time.Second):
			close(out.release)
			t.Fatalf("%s cancelled in the queue waited for the frame before it", c.name)
		}
		requestStage = old
		close(out.release)
		if err := <-first; err != nil {
			t.Fatal(err)
		}
		out.mu.Lock()
		if f, err := proto.Read(bufio.NewReader(bytes.NewReader(out.got.Bytes()))); err != nil || f.Type != proto.TPing || out.got.Len() != 5 {
			t.Errorf("%s: the host got %q, want the ping alone", c.name, out.got.Bytes())
		}
		out.mu.Unlock()
		if len(conn.pending) != 0 {
			t.Errorf("%s: %d requests wait for an answer", c.name, len(conn.pending))
		}
	}
}

// An upload that a cancel stops, before its begin, a chunk or its end,
// sends no frame from that one on: the frames before the cancel go, and no
// other. A cancel once all are out ends the wait for the answer. Either
// way, no request id is left waiting.
func TestAnUploadStopsAtItsCancel(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 300<<10) // two chunks
	for _, c := range []struct {
		stage string
		at    int // which time the stage comes
		sent  []proto.Type
	}{
		{"upload begin", 1, nil},
		{"upload chunk", 1, []proto.Type{proto.TUploadBegin}},
		{"upload chunk", 2, []proto.Type{proto.TUploadBegin, proto.TUploadChunk}},
		{"upload end", 1, []proto.Type{proto.TUploadBegin, proto.TUploadChunk, proto.TUploadChunk}},
		{"upload sent", 1, []proto.Type{proto.TUploadBegin, proto.TUploadChunk, proto.TUploadChunk, proto.TUploadEnd}},
	} {
		var out bytes.Buffer
		conn := &Conn{w: proto.NewWriter(&out), pending: map[uint64]chan proto.Response{}, done: make(chan struct{})}
		ctx, cancel := context.WithCancel(context.Background())
		seen := 0
		old := requestStage
		requestStage = func(stage string) {
			if stage == c.stage {
				if seen++; seen == c.at {
					cancel()
				}
			}
		}
		_, err := conn.Upload(ctx, "s1", "a.png", data)
		requestStage = old
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Errorf("%s %d: %v", c.stage, c.at, err)
		}
		var sent []proto.Type
		for r := bufio.NewReader(&out); ; {
			f, err := proto.Read(r)
			if err != nil {
				break
			}
			sent = append(sent, f.Type)
		}
		if !slices.Equal(sent, c.sent) {
			t.Errorf("a cancel at %s %d: the host got %v, want %v", c.stage, c.at, sent, c.sent)
		}
		if len(conn.pending) != 0 {
			t.Errorf("%s %d: %d requests wait for an answer", c.stage, c.at, len(conn.pending))
		}
	}
}

// SaveHosts, the one writer of hosts.toml, saves nothing once its context
// has ended: after a cancel, the error is a cancel (exit code 130), and
// after a timeout, a failure. The file stays as it was.
func TestNoSaveAfterACancel(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := SaveHostsContext(context.Background(), []Host{{Name: "kept", SSH: "kept"}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithTimeout(context.Background(), -time.Second)
	defer stop()
	for _, c := range []struct {
		ctx  context.Context
		want error
		kind Kind
	}{{ctx, context.Canceled, KindCancelled}, {expired, context.DeadlineExceeded, KindFailed}} {
		err := SaveHostContext(c.ctx, Host{Name: "new", SSH: "new"})
		var ue *UserError
		if !errors.Is(err, c.want) || !errors.As(err, &ue) || ue.Kind != c.kind || ue.Hint == "" {
			t.Errorf("SaveHost after %v = %v (%+v)", c.want, err, ue)
		}
		if err := SaveHostsContext(c.ctx, nil); !errors.Is(err, c.want) {
			t.Errorf("SaveHosts after %v = %v", c.want, err)
		}
	}
	if hosts, err := LoadHosts(); err != nil || len(hosts) != 1 || hosts[0].Name != "kept" {
		t.Errorf("the hosts after the cancels: %+v, %v", hosts, err)
	}
}
