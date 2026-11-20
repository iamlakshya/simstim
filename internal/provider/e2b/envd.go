package e2b

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/ring"
)

// EnvdPort is the port of envd, E2B's daemon inside each sandbox.
const EnvdPort = 49983

// chunk is the most stdin sent in one message, as the E2B CLI sends.
const chunk = 64 << 10

// Envd runs commands in one sandbox through envd's process service
// (process.proto in e2b-dev/E2B, Apache-2.0). Start carries stdout as a
// server stream; stdin goes in ordered SendInput calls, or in one
// StreamInput client stream. It never sends a Connect-Timeout-Ms header,
// which envd would make the lifetime of the process.
type Envd struct {
	// URL is envd's base URL: https://49983-<sandbox>.<domain>.
	URL string
	// SandboxID names the sandbox, for the edge's routing headers.
	SandboxID string
	// Token is the sandbox's envd access token. It is fetched from the API on
	// each connect and kept in memory only.
	Token string
	// User is the Linux user that commands run as.
	User string
	// Stream sends stdin in one StreamInput request instead of one
	// SendInput request for each chunk.
	Stream bool
	// Host names the host, for hints.
	Host string
	// HTTP sends the requests.
	HTTP *http.Client
}

type processConfig struct {
	Cmd  string            `json:"cmd"`
	Args []string          `json:"args,omitempty"`
	Envs map[string]string `json:"envs,omitempty"`
	Cwd  string            `json:"cwd,omitempty"`
}

type startRequest struct {
	Process processConfig `json:"process"`
	Tag     string        `json:"tag,omitempty"`
	Stdin   bool          `json:"stdin"`
}

type selector struct {
	Pid uint32 `json:"pid,omitempty"`
	Tag string `json:"tag,omitempty"`
}

type processInput struct {
	Stdin []byte `json:"stdin"`
}

type sendInputRequest struct {
	Process selector     `json:"process"`
	Input   processInput `json:"input"`
}

type streamInputRequest struct {
	Start *struct {
		Process selector `json:"process"`
	} `json:"start,omitempty"`
	Data *struct {
		Input processInput `json:"input"`
	} `json:"data,omitempty"`
}

type signalRequest struct {
	Process selector `json:"process"`
	Signal  string   `json:"signal"`
}

// processEvent is one message of the Start stream.
type processEvent struct {
	Event struct {
		Start *struct {
			Pid uint32 `json:"pid"`
		} `json:"start"`
		Data *struct {
			Stdout []byte `json:"stdout"`
			Stderr []byte `json:"stderr"`
		} `json:"data"`
		End *struct {
			ExitCode int32  `json:"exitCode"`
			Exited   bool   `json:"exited"`
			Status   string `json:"status"`
			Error    string `json:"error"`
		} `json:"end"`
	} `json:"event"`
}

func (e *Envd) request(ctx context.Context, method string, body io.Reader, stream bool) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(e.URL, "/")+"/process.Process/"+method, body)
	if err != nil {
		return nil, fmt.Errorf("envd %s: %w", method, err)
	}
	if stream {
		req.Header.Set("Content-Type", contentStream)
		// envd sends a keepalive event this often, so the edge sees traffic.
		req.Header.Set("Keepalive-Ping-Interval", "50")
	} else {
		req.Header.Set("Content-Type", contentUnary)
	}
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("X-Access-Token", e.Token)
	req.Header.Set("E2b-Sandbox-Id", e.SandboxID)
	req.Header.Set("E2b-Sandbox-Port", strconv.Itoa(EnvdPort))
	req.Header.Set("User-Agent", brand.Name)
	if e.User != "" {
		req.SetBasicAuth(e.User, "")
	}
	return req, nil
}

func (e *Envd) client() *http.Client {
	if e.HTTP != nil {
		return e.HTTP
	}
	return http.DefaultClient
}

// unary calls one of envd's unary methods.
func (e *Envd) unary(ctx context.Context, method string, in any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("envd %s: encode the request: %w", method, err)
	}
	req, err := e.request(ctx, method, bytes.NewReader(b), false)
	if err != nil {
		return err
	}
	resp, err := e.client().Do(req)
	if err != nil {
		return fmt.Errorf("envd %s: %w", method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("envd %s: %w", method, responseError(resp))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return nil
}

// KeepAlive tells the client to ping: envd keeps a process running when its
// request drops, so the bridge ends itself when the pings stop.
func (e *Envd) KeepAlive() time.Duration { return 10 * time.Second }

// ReachHint says how to check the sandbox.
func (e *Envd) ReachHint() string {
	return brand.Name + " host status " + e.Host + "   (is the sandbox running?)"
}

// Start runs remote with /bin/sh -c in the sandbox, with a tag of its own
// and stdin open.
func (e *Envd) Start(ctx context.Context, remote string) (client.Proc, error) {
	return e.start(ctx, remote, startOptions{})
}

type startOptions struct {
	stderrTail bool
}

func (e *Envd) start(ctx context.Context, remote string, options startOptions) (client.Proc, error) {
	if err := ctx.Err(); err != nil {
		return nil, withCause(ctx, fmt.Errorf("cannot reach the sandbox %s: %w", e.SandboxID, err))
	}
	tag := brand.Name + "-" + randomHex(6)
	msg, err := json.Marshal(startRequest{Process: processConfig{Cmd: "/bin/sh", Args: []string{"-c", remote}}, Tag: tag, Stdin: true})
	if err != nil {
		return nil, fmt.Errorf("envd Start: encode the request: %w", err)
	}
	var body bytes.Buffer
	if err := writeEnvelope(&body, 0, msg); err != nil {
		return nil, err
	}
	sctx, cancel := context.WithCancel(ctx)
	var sent atomic.Bool
	req, err := e.request(sctx, "Start", &sentReader{r: &body, sent: &sent}, true)
	if err != nil {
		cancel()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		cancel()
		return nil, withCause(ctx, fmt.Errorf("cannot reach the sandbox %s: %w", e.SandboxID, err))
	}
	p := &proc{e: e, tag: tag, cancel: cancel, done: make(chan struct{})}
	if options.stderrTail {
		p.runStderr = ring.New(stderrTailBytes)
	}
	resp, err := e.client().Do(req)
	if err != nil {
		cancel()
		if sent.Load() {
			p.signal(ctx)
		}
		return nil, withCause(ctx, fmt.Errorf("cannot reach the sandbox %s: %w", e.SandboxID, err))
	}
	if resp.StatusCode != http.StatusOK {
		err := responseError(resp)
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("cannot start a command in the sandbox %s: %w", e.SandboxID, err)
	}
	p.body = resp.Body
	mayRun, err := p.readStart()
	if err != nil {
		cancel()
		resp.Body.Close()
		if mayRun {
			p.signal(ctx)
		}
		return nil, withCause(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		cancel()
		resp.Body.Close()
		p.signal(ctx)
		return nil, withCause(ctx, fmt.Errorf("cannot reach the sandbox %s: %w", e.SandboxID, err))
	}
	p.stdoutR, p.stdoutW = io.Pipe()
	if e.Stream {
		p.in = newStreamWriter(sctx, p)
	} else {
		p.in = newSendWriter(sctx, p)
	}
	go p.pump()
	// A dropped request does not end the process in the sandbox, so the end
	// of ctx kills it.
	context.AfterFunc(ctx, p.Kill)
	return p, nil
}

// sentReader notes when the transport begins to read the Start body, which
// holds the command and its tag. Before that, envd cannot have seen either.
type sentReader struct {
	r    io.Reader
	sent *atomic.Bool
}

func (r *sentReader) Read(b []byte) (int, error) {
	r.sent.Store(true)
	return r.r.Read(b) //nolint:wrapcheck // request body passes its reader's errors on as they are
}

func withCause(ctx context.Context, err error) error {
	if cause := context.Cause(ctx); cause != nil && !errors.Is(err, cause) {
		return fmt.Errorf("%w: %w", cause, err)
	}
	return err
}

// proc is one command running in the sandbox.
type proc struct {
	e      *Envd
	tag    string
	pid    uint32
	body   io.ReadCloser
	cancel func()
	in     io.WriteCloser

	stdoutR *io.PipeReader
	stdoutW *io.PipeWriter

	mu     sync.Mutex
	stderr strings.Builder
	// runStderr is allocated only for Run, before the output pump starts.
	runStderr *ring.Ring
	exit      *int32
	waitErr   error
	// inputErr is why the input failed, when that made send kill the
	// command.
	inputErr *inputError
	done     chan struct{}
	killed   sync.Once
	// signaled sends the one SIGKILL request, apart from the cut of the
	// stream, so every path that kills shares it.
	signaled sync.Once
	// killing is set when a kill starts, before it cuts any request, so an
	// input that fails after it failed because of the kill.
	killing atomic.Bool
}

func (p *proc) Stdin() io.WriteCloser { return p.in }
func (p *proc) Stdout() io.Reader     { return p.stdoutR }

func (p *proc) Stderr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stderr.String()
}

// readStart reads the first event, which carries the process ID. mayRun is
// true when the failure does not prove that envd rejected the command before
// starting it.
func (p *proc) readStart() (mayRun bool, err error) {
	for {
		flags, msg, err := readEnvelope(p.body)
		if err != nil {
			return true, fmt.Errorf("cannot reach the sandbox %s: %w", p.e.SandboxID, err)
		}
		if flags&flagEndStream != 0 {
			if err := endError(msg); err != nil {
				return false, fmt.Errorf("cannot start a command in the sandbox %s: %w", p.e.SandboxID, err)
			}
			return true, errors.New("envd ended the stream before the command started")
		}
		var ev processEvent
		if err := json.Unmarshal(msg, &ev); err != nil {
			return true, fmt.Errorf("read envd's start event: %w", err)
		}
		if ev.Event.Start != nil {
			p.pid = ev.Event.Start.Pid
			return false, nil
		}
	}
}

// pump copies the stream's stdout to the reader, keeps stderr, and records
// how the command ended.
func (p *proc) pump() {
	defer close(p.done)
	defer p.body.Close()
	for {
		flags, msg, err := readEnvelope(p.body)
		if err != nil {
			p.finish(fmt.Errorf("%w: the stream from the sandbox broke: %w", client.ErrUnreachable, err))
			return
		}
		if flags&flagEndStream != 0 {
			p.finish(endError(msg))
			return
		}
		var ev processEvent
		if err := json.Unmarshal(msg, &ev); err != nil {
			p.finish(fmt.Errorf("read an envd event: %w", err))
			return
		}
		switch {
		case ev.Event.Data != nil:
			if len(ev.Event.Data.Stderr) > 0 {
				p.captureStderr(ev.Event.Data.Stderr)
			}
			if len(ev.Event.Data.Stdout) > 0 {
				if _, err := p.stdoutW.Write(ev.Event.Data.Stdout); err != nil {
					p.finish(err)
					return
				}
			}
		case ev.Event.End != nil:
			code := ev.Event.End.ExitCode
			p.mu.Lock()
			p.exit = &code
			p.mu.Unlock()
		}
	}
}

// finish closes stdout and sets what Wait returns.
func (p *proc) finish(streamErr error) {
	p.mu.Lock()
	switch {
	case p.exit != nil && *p.exit == 0:
		p.waitErr = nil
	case p.exit != nil:
		p.waitErr = fmt.Errorf("exit status %d", *p.exit)
	case streamErr != nil:
		p.waitErr = streamErr
	default:
		p.waitErr = fmt.Errorf("%w: the sandbox ended the stream without an exit status", client.ErrUnreachable)
	}
	p.mu.Unlock()
	_ = p.stdoutW.Close()
}

func (p *proc) Wait() error {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitErr
}

// Kill ends the command: SIGKILL through envd, and the stream is dropped.
func (p *proc) Kill() { p.kill(context.Background()) }

// kill is Kill with ctx's values. The SIGKILL goes out even when ctx has
// ended, which is when a command most needs it. A second kill waits for the
// first.
func (p *proc) kill(ctx context.Context) {
	p.killing.Store(true)
	p.killed.Do(func() {
		p.signal(ctx)
		p.cancel()
		if p.stdoutR != nil {
			_ = p.stdoutR.CloseWithError(io.ErrClosedPipe)
		}
	})
}

// signal sends SIGKILL through envd once for the command, whichever path
// kills it: the end of ctx, a failed input, or Run itself. A call while the
// request is out waits for its answer. It keeps the stream, which then
// carries the command's end event.
func (p *proc) signal(ctx context.Context) {
	p.signaled.Do(func() { p.sendSignal(ctx) })
}

// sendSignal asks envd for the SIGKILL, unless the command's exit status is
// known. It uses the process ID once StartEvent has given one, and the
// request's tag before then. Only signal calls it.
func (p *proc) sendSignal(ctx context.Context) {
	if p.exited() {
		return
	}
	sel := selector{Pid: p.pid}
	if sel.Pid == 0 {
		sel = selector{Tag: p.tag}
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = p.e.unary(sctx, "SendSignal", signalRequest{Process: sel, Signal: "SIGNAL_SIGKILL"})
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
