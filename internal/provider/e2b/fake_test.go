package e2b

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

const (
	testKey   = "e2b_fake_key"
	testID    = "i1a2b3c4d5e6f7g8h9j0k"
	testToken = "fake-envd-token"
)

// fixtures reads testdata/api.json, with {{brand}} as the product name.
func fixtures(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "api.json"))
	if err != nil {
		t.Fatal(err)
	}
	b = bytes.ReplaceAll(b, []byte("{{brand}}"), []byte(brand.Name))
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

type fixture struct {
	Method   string          `json:"method"`
	Path     string          `json:"path"`
	Request  json.RawMessage `json:"request"`
	Status   int             `json:"status"`
	Response json.RawMessage `json:"response"`
}

func entry(t *testing.T, all map[string]json.RawMessage, name string) fixture {
	t.Helper()
	var f fixture
	if err := json.Unmarshal(all[name], &f); err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return f
}

type call struct {
	Method, Path string
	Body         map[string]any
}

// fakeAPI answers the E2B REST API from the recorded shapes, and keeps the
// sandbox's state.
type fakeAPI struct {
	t     *testing.T
	all   map[string]json.RawMessage
	mu    sync.Mutex
	state string // "", running, paused, gone
	calls []call
	tmpl  string // the templates fixture to serve
}

func newFakeAPI(t *testing.T) (*fakeAPI, *httptest.Server) {
	f := &fakeAPI{t: t, all: fixtures(t), tmpl: "templates"}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeAPI) record(r *http.Request) {
	var body map[string]any
	if b, _ := io.ReadAll(r.Body); len(b) > 0 {
		if err := json.Unmarshal(b, &body); err != nil {
			f.t.Errorf("%s %s: the body is not JSON: %q", r.Method, r.URL.Path, b)
		}
	}
	f.mu.Lock()
	f.calls = append(f.calls, call{r.Method, r.URL.Path, body})
	f.mu.Unlock()
}

func (f *fakeAPI) Calls() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]call(nil), f.calls...)
}

func (f *fakeAPI) answer(w http.ResponseWriter, name string, status int) {
	e := entry(f.t, f.all, name)
	if status == 0 {
		status = e.Status
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if len(e.Response) > 0 && status != http.StatusNoContent {
		_, _ = w.Write(e.Response)
	}
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.record(r)
	if r.Header.Get("X-API-Key") != testKey {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":401,"message":"Invalid API key"}`))
		return
	}
	f.mu.Lock()
	state := f.state
	f.mu.Unlock()
	set := func(s string) { f.mu.Lock(); f.state = s; f.mu.Unlock() }
	sb := "/sandboxes/" + testID
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v2/sandboxes":
		set("running")
		f.answer(w, "create", 0)
	case r.Method == http.MethodGet && r.URL.Path == "/templates":
		f.answer(w, f.tmpl, 0)
	case state == "gone" || state == "":
		f.answer(w, "not_found", 0)
	case r.Method == http.MethodGet && r.URL.Path == sb:
		f.answer(w, "get_"+state, 0)
	case r.Method == http.MethodPost && r.URL.Path == "/v2"+sb+"/connect":
		set("running")
		if state == "running" {
			f.answer(w, "connect", http.StatusOK)
		} else {
			f.answer(w, "connect", 0)
		}
	case r.Method == http.MethodPost && r.URL.Path == sb+"/pause":
		set("paused")
		f.answer(w, "pause", 0)
	case r.Method == http.MethodPost && r.URL.Path == sb+"/timeout":
		f.answer(w, "timeout", 0)
	case r.Method == http.MethodDelete && r.URL.Path == sb:
		set("gone")
		f.answer(w, "kill", 0)
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	}
}

// fakeEnvd runs commands on this machine behind envd's process service, and
// checks every request's headers. A command outlives its request, as in envd.
// When the fake shuts down, every command ends with each child it left in its
// process group, and every handler and bridge ends.
type fakeEnvd struct {
	t   *testing.T
	srv *httptest.Server
	// ctx ends when the fake shuts down. It is the base of every request's
	// context, and it ends every command, bridge and handler.
	ctx    context.Context
	stop   context.CancelFunc
	run    running
	closed atomic.Bool
	done   chan struct{}
	// wait is how long the shutdown may take: shutdownWait, unless a test
	// of the shutdown itself sets it before it starts a command.
	wait time.Duration
	// afterClose, when set, runs in the shutdown once the connections are
	// closed, before it waits: a test of the shutdown sends requests there.
	afterClose func()

	mu    sync.Mutex
	procs map[uint32]*fakeCmd
	stdin map[uint32]io.WriteCloser
	paths []string
	tags  []string
	sigs  []string
	// writing counts the writes to a command's stdin in progress.
	writing int
	// wrapperErrs are the wrappers that did not exit well.
	wrapperErrs []string
	// failLeadGroup, when set, makes the wrapper fail leadGroup on Unix.
	failLeadGroup bool
	// drop, when set, makes Start end the stream right after the start
	// event, as a dropped connection would.
	drop bool
	// bridge, when set, serves commands that run the helper's bridge in
	// this process, instead of running them.
	bridge  func(in io.Reader, out io.Writer, remote string) error
	bridges []string
	// home, when set, is HOME for the commands it runs.
	home string
	// gate, when set, holds each SendInput call, and the start of each
	// StreamInput request, until it is closed or a value comes, or the
	// request ends.
	gate chan struct{}
	// parts are the sizes of the SendInput calls, in order.
	parts []int
	// failEOF, when set, makes every CloseStdin call fail, as a proxy in
	// front of envd may.
	failEOF bool
	// holdEOF, when set, holds every CloseStdin call until its request ends.
	holdEOF bool
	// cutStream, when set, ends a StreamInput request with an error once it
	// has taken that many bytes of input.
	cutStream int
	// stopStream, when set, makes a StreamInput request stop reading once
	// it has taken that many bytes of input. It keeps the connection until
	// the request ends, and counts itself in streamStopped while it waits.
	stopStream    int
	streamStopped atomic.Int32
	// wantHTTP2, when set, checks that every request arrives over HTTP/2.
	wantHTTP2 bool
	// streamRate, when set, makes a StreamInput request take its body at
	// that many bytes a second, through a buffer of 1 MiB, as the far end
	// of a slow link does.
	streamRate int
	// pipeGate holds output readers after StartEvent, until a test releases
	// them or the request ends.
	pipeGate chan struct{}
}

// serveBridge runs the bridge in this process, as if it were a command in
// the sandbox, with a made-up process ID.
func (f *fakeEnvd) serveBridge(w http.ResponseWriter, remote string, bridge func(io.Reader, io.Writer, string) error) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	f.mu.Lock()
	pid := uint32(1_000_000 + len(f.bridges))
	f.stdin[pid] = inW
	f.bridges = append(f.bridges, remote)
	f.mu.Unlock()
	// The shutdown ends the bridge's input, and this handler's read of its
	// output, even when the bridge does not stop. When the handler ends, so
	// do the pipes' other ends.
	stop := f.onShutdown(func() {
		_ = inW.CloseWithError(errShutDown)
		_ = outR.CloseWithError(errShutDown)
	})
	defer func() {
		stop()
		_ = inW.CloseWithError(errBridgeEnded)
		_ = outR.CloseWithError(errBridgeEnded)
	}()
	w.Header().Set("Content-Type", contentStream)
	w.WriteHeader(http.StatusOK)
	fw := &flushWriter{w: w}
	fw.send(0, map[string]any{"event": map[string]any{"start": map[string]any{"pid": pid}}})
	ended := f.run.add(fmt.Sprintf("the bridge %d", pid))
	go func() {
		defer ended()
		_ = bridge(inR, outW, remote)
		// A bridge that ends takes no more input: a SendInput then fails at
		// once instead of waiting for a reader.
		_ = inR.CloseWithError(errBridgeEnded)
		_ = outW.Close()
	}()
	buf := make([]byte, 32<<10)
	for {
		n, err := outR.Read(buf)
		if n > 0 {
			fw.send(0, map[string]any{"event": map[string]any{"data": map[string]any{"stdout": buf[:n]}}})
		}
		if err != nil {
			break
		}
	}
	fw.send(0, map[string]any{"event": map[string]any{"end": map[string]any{"exited": true}}})
	fw.send(flagEndStream, map[string]any{})
}

func newFakeEnvd(t *testing.T) (*fakeEnvd, *httptest.Server) {
	f := &fakeEnvd{t: t, wait: shutdownWait, done: make(chan struct{}), procs: map[uint32]*fakeCmd{}, stdin: map[uint32]io.WriteCloser{}}
	f.ctx, f.stop = context.WithCancel(context.Background())
	f.srv = httptest.NewUnstartedServer(f)
	f.srv.Config.BaseContext = func(net.Listener) context.Context { return f.ctx }
	f.srv.Start()
	t.Cleanup(func() {
		if err := f.shutdown(); err != nil {
			t.Error(err)
		}
	})
	return f, f.srv
}

func (f *fakeEnvd) Paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

func (f *fakeEnvd) check(r *http.Request, stream bool) bool {
	f.mu.Lock()
	f.paths = append(f.paths, strings.TrimPrefix(r.URL.Path, "/process.Process/"))
	f.mu.Unlock()
	want := contentUnary
	if stream {
		want = contentStream
	}
	user, _, ok := r.BasicAuth()
	switch {
	case !strings.HasPrefix(r.Header.Get("X-Access-Token"), testToken): // a resume may hand out a new one
		f.t.Errorf("%s: X-Access-Token = %q", r.URL.Path, r.Header.Get("X-Access-Token"))
	case r.Header.Get("Content-Type") != want:
		f.t.Errorf("%s: Content-Type = %q, want %q", r.URL.Path, r.Header.Get("Content-Type"), want)
	case r.Header.Get("Connect-Timeout-Ms") != "":
		f.t.Errorf("%s: a Connect-Timeout-Ms header was sent; envd would kill the process when it ends", r.URL.Path)
	case !ok || user != "user":
		f.t.Errorf("%s: basic auth user = %q", r.URL.Path, user)
	case r.Header.Get("E2b-Sandbox-Id") != testID:
		f.t.Errorf("%s: E2b-Sandbox-Id = %q", r.URL.Path, r.Header.Get("E2b-Sandbox-Id"))
	case f.wantHTTP2 && r.ProtoMajor != 2:
		f.t.Errorf("%s: proto = %s, want HTTP/2", r.URL.Path, r.Proto)
	default:
		return true
	}
	return false
}

func (f *fakeEnvd) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := strings.TrimPrefix(r.URL.Path, "/process.Process/")
	defer f.run.add("the " + method + " handler")()
	switch method {
	case "Start":
		if f.check(r, true) {
			f.start(w, r)
		}
	case "StreamInput":
		if f.check(r, true) {
			f.streamInput(w, r)
		}
	case "SendInput", "CloseStdin", "SendSignal":
		if f.check(r, false) {
			f.unary(w, r)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

type flushWriter struct {
	mu sync.Mutex
	w  http.ResponseWriter
}

func (fw *flushWriter) send(flags byte, v any) {
	b, _ := json.Marshal(v)
	fw.mu.Lock()
	defer fw.mu.Unlock()
	_ = writeEnvelope(fw.w, flags, b)
	fw.w.(http.Flusher).Flush()
}

func (f *fakeEnvd) start(w http.ResponseWriter, r *http.Request) {
	_, msg, err := readEnvelope(r.Body)
	if err != nil {
		f.requestError(r, err)
		return
	}
	var req startRequest
	if err := json.Unmarshal(msg, &req); err != nil {
		f.t.Errorf("Start: %v", err)
		return
	}
	if req.Process.Cmd != "/bin/sh" || len(req.Process.Args) != 2 || req.Process.Args[0] != "-c" || !req.Stdin || !strings.HasPrefix(req.Tag, brand.Name+"-") {
		f.t.Errorf("Start request = %+v", req)
	}
	if r.Header.Get("Keepalive-Ping-Interval") == "" {
		f.t.Error("Start: no Keepalive-Ping-Interval")
	}
	f.mu.Lock()
	bridge, home, failGroup := f.bridge, f.home, f.failLeadGroup
	f.mu.Unlock()
	if bridge != nil && strings.Contains(req.Process.Args[1], " bridge") {
		f.serveBridge(w, req.Process.Args[1], bridge)
		return
	}
	c, err := startWrapped(req.Process.Args[1], home, failGroup)
	if err != nil {
		f.t.Errorf("Start: %v", err)
		return
	}
	pid := c.pid()
	name := fmt.Sprintf("the command %d (%s)", pid, short(req.Process.Args[1]))
	// envd keeps a command running when its request drops, so only the
	// shutdown ends it, with every child it left behind, and its input.
	ended := f.run.add(name)
	f.onShutdown(func() {
		c.release()
		_ = c.stdin.Close()
	})
	go func() {
		defer ended()
		// The wrapper exits 0. A race or a panic in it gives another status,
		// with its report in the test's output.
		if err := c.wrapper.Wait(); err != nil {
			f.mu.Lock()
			f.wrapperErrs = append(f.wrapperErrs, fmt.Sprintf("the wrapper of %s: %v", name, err))
			f.mu.Unlock()
		}
	}()
	f.mu.Lock()
	f.procs[pid], f.stdin[pid] = c, c.stdin
	f.tags = append(f.tags, req.Tag)
	drop := f.drop
	f.mu.Unlock()
	w.Header().Set("Content-Type", contentStream)
	w.WriteHeader(http.StatusOK)
	fw := &flushWriter{w: w}
	fw.send(0, map[string]any{"event": map[string]any{"start": map[string]any{"pid": pid}}})
	fw.send(0, map[string]any{"event": map[string]any{"keepalive": map[string]any{}}})
	if drop {
		// The handler returns: the stream ends with no end event. The
		// command runs on, and the shutdown still waits for its output.
		f.drainDropped(name, c)
		return
	}
	f.mu.Lock()
	pipeGate := f.pipeGate
	f.mu.Unlock()
	if pipeGate != nil {
		select {
		case <-pipeGate:
		case <-r.Context().Done():
			f.drainDropped(name, c)
			return
		}
	}
	defer f.run.add("the stream of " + name)()
	var wg sync.WaitGroup
	pipe := func(rd io.Reader, field string) {
		defer wg.Done()
		br := bufio.NewReader(rd)
		buf := make([]byte, 32<<10)
		for {
			n, err := br.Read(buf)
			if n > 0 {
				fw.send(0, map[string]any{"event": map[string]any{"data": map[string]any{field: buf[:n]}}})
			}
			if err != nil {
				return
			}
		}
	}
	wg.Add(2)
	go pipe(c.stdout, "stdout")
	go pipe(c.stderr, "stderr")
	wg.Wait()
	c.closeOutput()
	<-c.ended
	fw.send(0, map[string]any{"event": map[string]any{"end": endEvent(c.status)}})
	fw.send(flagEndStream, map[string]any{})
}

func (f *fakeEnvd) drainDropped(name string, c *fakeCmd) {
	drained := f.run.add("the dropped stream of " + name)
	go func() {
		defer drained()
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = io.Copy(io.Discard, c.stdout) }()
		go func() { defer wg.Done(); _, _ = io.Copy(io.Discard, c.stderr) }()
		wg.Wait()
		c.closeOutput()
	}()
}

// input writes to a command's stdin, and fails as envd does: not_found for a
// process that envd does not know, and internal for a write that fails, as
// to a command that has ended but whose end envd has not passed on yet.
func (f *fakeEnvd) input(pid uint32, data []byte) *rpcError {
	f.mu.Lock()
	in := f.stdin[pid]
	f.mu.Unlock()
	if in == nil {
		return &rpcError{Code: "not_found", Message: fmt.Sprintf("process with pid %d not found", pid)}
	}
	f.mu.Lock()
	f.writing++
	f.mu.Unlock()
	_, err := in.Write(data)
	f.mu.Lock()
	f.writing--
	f.mu.Unlock()
	if err == nil {
		return nil
	}
	return &rpcError{Code: "internal", Message: "error writing to stdin: " + err.Error()}
}

// status is the HTTP status that envd's Connect protocol gives an error.
func status(e *rpcError) int {
	if e.Code == "not_found" {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

func (f *fakeEnvd) unary(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Process selector     `json:"process"`
		Input   processInput `json:"input"`
		Signal  string       `json:"signal"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		f.requestError(r, err)
		return
	}
	pid := req.Process.Pid
	var failed *rpcError
	switch strings.TrimPrefix(r.URL.Path, "/process.Process/") {
	case "SendInput":
		f.mu.Lock()
		gate := f.gate
		f.parts = append(f.parts, len(req.Input.Stdin))
		f.mu.Unlock()
		if gate != nil {
			select {
			case <-gate:
			case <-r.Context().Done():
			}
		}
		failed = f.input(pid, req.Input.Stdin)
	case "CloseStdin":
		f.mu.Lock()
		failEOF, holdEOF := f.failEOF, f.holdEOF
		if in := f.stdin[pid]; in != nil && !failEOF && !holdEOF {
			_ = in.Close()
		}
		f.mu.Unlock()
		if holdEOF {
			<-r.Context().Done()
			return
		}
		if failEOF {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("no healthy upstream\n"))
			return
		}
	case "SendSignal":
		f.mu.Lock()
		f.sigs = append(f.sigs, req.Signal)
		c := f.procs[pid]
		f.mu.Unlock()
		if c != nil && req.Signal == "SIGNAL_SIGKILL" {
			// As in envd, the command dies first. Then its stdin and output
			// close, so its stream ends with the end event even while a child
			// of the command holds them. The child ends at the shutdown.
			c.kill()
			select {
			case <-c.ended:
			case <-time.After(5 * time.Second):
			}
			_ = c.stdin.Close()
			c.cutOutput()
		}
	}
	w.Header().Set("Content-Type", contentUnary)
	if failed != nil {
		w.WriteHeader(status(failed))
		_ = json.NewEncoder(w).Encode(failed)
		return
	}
	_, _ = w.Write([]byte("{}"))
}

func (f *fakeEnvd) streamInput(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	gate, cut, stop, rate := f.gate, f.cutStream, f.stopStream, f.streamRate
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-r.Context().Done():
		}
	}
	body := io.Reader(r.Body)
	if rate > 0 {
		body = rateBody(r.Context(), r.Body, rate, r.ProtoMajor < 2)
	}
	taken := 0
	var pid uint32
	for {
		_, msg, err := readEnvelope(body)
		if err != nil {
			break
		}
		var m streamInputRequest
		if err := json.Unmarshal(msg, &m); err != nil {
			f.t.Errorf("StreamInput: %v", err)
			return
		}
		switch {
		case m.Start != nil:
			pid = m.Start.Process.Pid
		case m.Data != nil:
			if cut > 0 && taken >= cut {
				// The stream ends with an error part way, as when a link breaks.
				w.Header().Set("Content-Type", contentStream)
				w.WriteHeader(http.StatusOK)
				(&flushWriter{w: w}).send(flagEndStream, map[string]any{"error": rpcError{Code: "unavailable", Message: "the link broke"}})
				return
			}
			if stop > 0 && taken >= stop {
				f.streamStopped.Add(1)
				defer f.streamStopped.Add(-1)
				<-r.Context().Done() // envd reads no more, and keeps the connection
				return
			}
			taken += len(m.Data.Input.Stdin)
			if failed := f.input(pid, m.Data.Input.Stdin); failed != nil {
				// A shutdown, or a client that went away, cut the input.
				// Otherwise envd ends the stream with the error.
				if !f.stopping(r) {
					w.Header().Set("Content-Type", contentStream)
					w.WriteHeader(http.StatusOK)
					(&flushWriter{w: w}).send(flagEndStream, map[string]any{"error": failed})
				}
				return
			}
		}
	}
	if f.stopping(r) {
		return
	}
	w.Header().Set("Content-Type", contentStream)
	w.WriteHeader(http.StatusOK)
	fw := &flushWriter{w: w}
	fw.send(0, map[string]any{})
	fw.send(flagEndStream, map[string]any{})
}
