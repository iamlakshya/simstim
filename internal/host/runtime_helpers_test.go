package host_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// start starts a session that runs argv in the home folder.
func start(t *testing.T, name string, argv ...string) *host.SessionInfo {
	t.Helper()
	s, err := host.New(host.NewArgs{Name: name, Cwd: "~", Argv: argv, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("new %s: %v", name, err)
	}
	return s
}

// peek returns the newest output of a session.
func peek(t *testing.T, id string) string {
	t.Helper()
	raw, err := host.HolderRequest(id, "peek", map[string]int{"bytes": 64 << 10}, 3*time.Second)
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	var r struct {
		Data []byte `json:"data"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return string(r.Data)
}

// waitOutput waits until a session's output holds want.
func waitOutput(t *testing.T, id, want string) {
	t.Helper()
	hosttest.WaitFor(t, 10*time.Second, "output "+want, func() bool { return strings.Contains(peek(t, id), want) })
}

// send types text into a session, with Enter.
func send(t *testing.T, id, text string) {
	t.Helper()
	if _, err := host.HolderRequest(id, "send", map[string]any{"text": text, "enter": true}, 3*time.Second); err != nil {
		t.Fatalf("send: %v", err)
	}
}

// manifest reads a session's manifest.
func manifest(t *testing.T, id string) *host.Manifest {
	t.Helper()
	m, err := host.LoadManifest(id)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// live returns the holder's own copy of a session's manifest, which is
// current: the file is saved only once a second.
func live(t *testing.T, id string) *host.Manifest {
	t.Helper()
	raw, err := host.HolderRequest(id, "info", nil, 3*time.Second)
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	var m host.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return &m
}

// waitState waits until a session's manifest has the state.
func waitState(t *testing.T, id, state string) {
	t.Helper()
	hosttest.WaitFor(t, 10*time.Second, "state "+state, func() bool {
		m, err := host.LoadManifest(id)
		return err == nil && m.Status.State == state
	})
}

// dial connects a client to this host, through the real bridge.
func dial(t *testing.T, h *hosttest.Host) *client.Conn {
	t.Helper()
	ch, _ := h.ClientHost("box")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := client.Dial(ctx, ch, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

// rawBridge is a client that speaks the bridge's frames itself, for what
// client.Conn hides.
type rawBridge struct {
	t      *testing.T
	w      *proto.Writer
	frames chan proto.Frame
	in     *io.PipeWriter
	done   chan error
}

func newRawBridge(t *testing.T) *rawBridge {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	b := &rawBridge{t: t, w: proto.NewWriter(inW), frames: make(chan proto.Frame, 1024), in: inW, done: make(chan error, 1)}
	go func() {
		err := host.RunBridge(inR, outW)
		_ = outW.CloseWithError(io.EOF)
		b.done <- err
	}()
	go func() {
		r := bufio.NewReader(outR)
		for {
			f, err := proto.Read(r)
			if err != nil {
				close(b.frames)
				return
			}
			b.frames <- f
		}
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		select {
		case <-b.done:
		case <-time.After(5 * time.Second):
			t.Error("the bridge did not end when its input closed")
		}
	})
	if err := b.w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Role: "client"}); err != nil {
		t.Fatal(err)
	}
	if f := b.next(proto.THello); !bytes.Contains(f.Payload, []byte(`"bridge"`)) {
		t.Fatalf("hello = %s", f.Payload)
	}
	return b
}

// next returns the next frame of one of the types, skipping others.
func (b *rawBridge) next(types ...proto.Type) proto.Frame {
	b.t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case f, ok := <-b.frames:
			if !ok {
				b.t.Fatalf("the bridge closed while waiting for %v", types)
			}
			for _, ty := range types {
				if f.Type == ty {
					return f
				}
			}
		case <-timeout:
			b.t.Fatalf("no frame of %v in time", types)
		}
	}
}

func (b *rawBridge) send(ty proto.Type, p []byte) {
	b.t.Helper()
	if err := b.w.Write(ty, p); err != nil {
		b.t.Fatal(err)
	}
}

func (b *rawBridge) sendJSON(ty proto.Type, v any) {
	b.t.Helper()
	if err := b.w.WriteJSON(ty, v); err != nil {
		b.t.Fatal(err)
	}
}

// request sends one request and returns its response.
func (b *rawBridge) request(id uint64, op string, args any) proto.Response {
	b.t.Helper()
	req := proto.Request{ID: id, Op: op}
	if args != nil {
		req.Args, _ = json.Marshal(args)
	}
	b.sendJSON(proto.TRequest, req)
	return b.response(id)
}

// response waits for the response with this ID.
func (b *rawBridge) response(id uint64) proto.Response {
	b.t.Helper()
	for {
		var r proto.Response
		if err := json.Unmarshal(b.next(proto.TResponse).Payload, &r); err != nil {
			b.t.Fatal(err)
		}
		if r.ID == id {
			return r
		}
	}
}

// attach attaches and returns the Attached frame.
func (b *rawBridge) attach(id string, last int64, cols, rows int) proto.Attached {
	b.t.Helper()
	b.sendJSON(proto.TAttach, proto.Attach{Session: id, Cols: cols, Rows: rows, LastOffset: last})
	f := b.next(proto.TAttached, proto.TError)
	if f.Type == proto.TError {
		b.t.Fatalf("attach failed: %s", f.Payload)
	}
	var a proto.Attached
	if err := json.Unmarshal(f.Payload, &a); err != nil {
		b.t.Fatal(err)
	}
	return a
}

// output collects output frames until the bytes from offset from hold want,
// and returns them.
func (b *rawBridge) output(from int64, want string) []byte {
	b.t.Helper()
	var got []byte
	for !bytes.Contains(got, []byte(want)) {
		f := b.next(proto.TOutput)
		off, data, err := proto.DecodeOutput(f.Payload)
		if err != nil {
			b.t.Fatal(err)
		}
		if off != from+int64(len(got)) {
			b.t.Fatalf("output at offset %d, want %d", off, from+int64(len(got)))
		}
		got = append(got, data...)
	}
	return got
}

// git runs git in dir, as a test user, and fails the test on an error.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "init.defaultBranch=main", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// writeFile writes a file, making its folder.
func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// holderRuns reports whether session id's holder runs.
func holderRuns(t *testing.T, id string) bool {
	t.Helper()
	list, err := host.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.ID == id {
			return s.Alive
		}
	}
	return false
}
