package egress

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"

	"github.com/iamlakshya/simstim/internal/api"
)

type secretRouteH2Frame struct {
	stream       uint32
	fields       []hpack.HeaderField
	data         []byte
	ended, reset bool
}

type secretRouteH2Peer struct {
	conn     net.Conn
	writer   *http2.Framer
	writeMu  sync.Mutex
	frames   chan secretRouteH2Frame
	failures chan error
	done     chan struct{}
}

// This peer sends real HTTP/2 frames. A zero stream window stops DATA for
// stream1 while a separate WINDOW_UPDATE allows only stream3 to progress.
func newSecretRouteH2Peer(t *testing.T, f *secretRouteFixture) *secretRouteH2Peer {
	t.Helper()
	conn, err := f.dialTLS(t.Context(), 2, "api.example.com", "api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	peer := &secretRouteH2Peer{conn: conn, writer: http2.NewFramer(conn, nil), frames: make(chan secretRouteH2Frame, 128), failures: make(chan error, 1), done: make(chan struct{})}
	reader := http2.NewFramer(nil, conn)
	reader.ReadMetaHeaders = hpack.NewDecoder(4096, nil)
	reader.MaxHeaderListSize = maxHTTPHeaderBytes
	go peer.read(reader)
	t.Cleanup(func() {
		_ = conn.Close()
		select {
		case <-peer.done:
		case <-time.After(5 * time.Second):
			t.Error("owned raw HTTP/2 reader did not join")
		}
	})
	peer.writeMu.Lock()
	_, err = io.WriteString(conn, http2.ClientPreface)
	if err == nil {
		err = peer.writer.WriteSettings(http2.Setting{ID: http2.SettingInitialWindowSize, Val: 0})
	}
	peer.writeMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return peer
}

func (p *secretRouteH2Peer) fail(err error) {
	select {
	case p.failures <- err:
	default:
	}
	_ = p.conn.Close()
}

func (p *secretRouteH2Peer) read(reader *http2.Framer) {
	defer close(p.done)
	for {
		frame, err := reader.ReadFrame()
		if err != nil {
			p.fail(err)
			return
		}
		out := secretRouteH2Frame{stream: frame.Header().StreamID}
		switch frame := frame.(type) {
		case *http2.SettingsFrame:
			if !frame.IsAck() {
				p.writeMu.Lock()
				err = p.writer.WriteSettingsAck()
				p.writeMu.Unlock()
				if err != nil {
					p.fail(err)
					return
				}
			}
			continue
		case *http2.PingFrame:
			if !frame.IsAck() {
				p.writeMu.Lock()
				err = p.writer.WritePing(true, frame.Data)
				p.writeMu.Unlock()
				if err != nil {
					p.fail(err)
					return
				}
			}
			continue
		case *http2.WindowUpdateFrame:
			continue
		case *http2.MetaHeadersFrame:
			if frame.Truncated {
				p.fail(errors.New("response header oracle was truncated"))
				return
			}
			out.fields = append([]hpack.HeaderField(nil), frame.Fields...)
			out.ended = frame.StreamEnded()
		case *http2.DataFrame:
			out.data = append([]byte(nil), frame.Data()...)
			out.ended = frame.StreamEnded()
		case *http2.RSTStreamFrame:
			out.reset = true
		case *http2.GoAwayFrame:
			p.fail(errors.New("request revocation closed the whole guest HTTP/2 connection"))
			return
		default:
			continue
		}
		select {
		case p.frames <- out:
		default:
			p.fail(errors.New("bounded HTTP/2 frame observer overflowed"))
			return
		}
	}
}

func (p *secretRouteH2Peer) request(t *testing.T, stream uint32, path, sentinel string) {
	t.Helper()
	var block bytes.Buffer
	encoder := hpack.NewEncoder(&block)
	for _, field := range []hpack.HeaderField{{Name: ":method", Value: "GET"}, {Name: ":scheme", Value: "https"}, {Name: ":authority", Value: secretRouteAuthority}, {Name: ":path", Value: path}, {Name: "authorization", Value: "Bearer " + sentinel}} {
		if err := encoder.WriteField(field); err != nil {
			t.Fatal(err)
		}
	}
	p.writeMu.Lock()
	err := p.writer.WriteHeaders(http2.HeadersFrameParam{StreamID: stream, BlockFragment: block.Bytes(), EndHeaders: true, EndStream: true})
	p.writeMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
}

func (p *secretRouteH2Peer) window(t *testing.T, stream, size uint32) {
	t.Helper()
	p.writeMu.Lock()
	err := p.writer.WriteWindowUpdate(stream, size)
	p.writeMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
}

func (p *secretRouteH2Peer) next(t *testing.T) secretRouteH2Frame {
	t.Helper()
	select {
	case frame := <-p.frames:
		return frame
	case err := <-p.failures:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("actual HTTP/2 stream did not progress")
	}
	return secretRouteH2Frame{}
}

func TestHTTPSecretRouteRevokesBlockedHTTP2WriterWithoutClosingItsSibling(t *testing.T) {
	blockedStarted := make(chan struct{}, 1)
	blockedEnded := make(chan struct{})
	siblingRelease := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(siblingRelease) }) }
	t.Cleanup(release)
	pol := secretRoutePolicy(t)
	f := newSecretRouteFixture(t, 2, 2, secretRouteArgs(t, pol), http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer "+secretRouteReal {
			http.Error(w, "wrong actual request credential", 400)
			return
		}
		if req.URL.Path == "/allowed/blocked" {
			defer close(blockedEnded)
			blockedStarted <- struct{}{}
			_, _ = io.WriteString(w, strings.Repeat("blocked output bytes\n", 32768))
			_ = http.NewResponseController(w).Flush()
			<-req.Context().Done()
			return
		}
		_, _ = io.WriteString(w, "ready|"+secretRouteReal+"|"+strings.Repeat("s", 512))
		if http.NewResponseController(w).Flush() != nil {
			return
		}
		select {
		case <-siblingRelease:
		case <-req.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "|survived")
	}))
	peer := newSecretRouteH2Peer(t, f)
	peer.request(t, 1, "/allowed/blocked", f.sentinel())
	peer.request(t, 3, "/allowed/sibling", f.sentinel())
	peer.window(t, 3, 65535)
	waitHTTPProgress(t, blockedStarted, "the actual blocked response origin")
	var sibling []byte
	blockedHeaders := false
	for !blockedHeaders || !bytes.HasPrefix(sibling, []byte("ready|"+f.sentinel()+"|")) {
		frame := peer.next(t)
		if bytes.Contains(frame.data, []byte(secretRouteReal)) {
			t.Fatal("real value escaped a multiplexed response")
		}
		for _, field := range frame.fields {
			if strings.Contains(field.Value, secretRouteReal) {
				t.Fatal("real value escaped HTTP/2 response metadata")
			}
			if frame.stream == 1 && field.Name == ":status" && field.Value == "200" {
				blockedHeaders = true
			}
		}
		if frame.stream == 1 && len(frame.data) > 0 {
			t.Fatal("blocked stream emitted DATA without receive-window credit")
		}
		if frame.stream == 3 {
			sibling = append(sibling, frame.data...)
			if frame.reset || frame.ended {
				t.Fatal("sibling ended before its independent release")
			}
		}
	}
	select {
	case <-blockedEnded:
		t.Fatal("zero-window response origin ended before revocation")
	default:
	}
	// No credit is ever granted to stream1. Publication must interrupt its
	// exact writer and origin while preserving the local TLS carrier.
	f.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: secretRoutePolicy(t, api.Rule{Allow: true, Target: secretRouteAuthority + "/allowed/sibling"})}, nil)
	waitHTTPProgress(t, blockedEnded, "the revoked origin before any stream1 window update")
	release()
	ended := false
	for !ended {
		frame := peer.next(t)
		if frame.stream != 3 {
			continue
		}
		if frame.reset {
			t.Fatal("revoking the blocked request reset its sibling")
		}
		sibling = append(sibling, frame.data...)
		ended = frame.ended
	}
	if string(sibling) != "ready|"+f.sentinel()+"|"+strings.Repeat("s", 512)+"|survived" || bytes.Contains(sibling, []byte(secretRouteReal)) {
		t.Fatal("sibling response lost exact safe bytes or leaked its real credential")
	}
	if f.guestDials.Load() != 1 || f.lookups.Load() != 2 || f.dials.Load() != 2 || f.requests.Load() != 2 {
		t.Fatal("stream revocation retried, redialled or closed the shared carrier")
	}
}
