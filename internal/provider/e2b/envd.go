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
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
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
	return brand.Name + " host status " + e.Host + " (says whether the sandbox runs)"
}

// Start runs remote with /bin/sh -c in the sandbox, with a tag of its own
// and stdin open.
func (e *Envd) Start(ctx context.Context, remote string) (client.Proc, error) {
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
	req, err := e.request(sctx, "Start", &body, true)
	if err != nil {
		cancel()
		return nil, err
	}
	resp, err := e.client().Do(req)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("cannot reach the sandbox %s: %w", e.SandboxID, err)
	}
	if resp.StatusCode != http.StatusOK {
		err := responseError(resp)
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("cannot start a command in the sandbox %s: %w", e.SandboxID, err)
	}
	p := &proc{e: e, tag: tag, body: resp.Body, cancel: cancel, done: make(chan struct{})}
	if err := p.readStart(); err != nil {
		cancel()
		resp.Body.Close()
		return nil, err
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

// Run runs remote to its end, with stdin, and returns its stdout.
func (e *Envd) Run(ctx context.Context, remote string, stdin io.Reader) ([]byte, error) {
	p, err := e.Start(ctx, remote)
	if err != nil {
		return nil, err
	}
	go func() {
		if stdin != nil {
			_, _ = io.Copy(p.Stdin(), stdin)
		}
		_ = p.Stdin().Close()
	}()
	out, rerr := io.ReadAll(p.Stdout())
	if werr := p.Wait(); werr != nil {
		return out, fmt.Errorf("%w: %s", werr, provider.Tail(p.Stderr(), 3))
	}
	if rerr != nil {
		return out, fmt.Errorf("read the command's output: %w", rerr)
	}
	return out, nil
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

	mu      sync.Mutex
	stderr  strings.Builder
	exit    *int32
	waitErr error
	done    chan struct{}
	killed  sync.Once
}

func (p *proc) Stdin() io.WriteCloser { return p.in }
func (p *proc) Stdout() io.Reader     { return p.stdoutR }

func (p *proc) Stderr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stderr.String()
}

// readStart reads the first event, which carries the process ID.
func (p *proc) readStart() error {
	for {
		flags, msg, err := readEnvelope(p.body)
		if err != nil {
			return fmt.Errorf("cannot reach the sandbox %s: %w", p.e.SandboxID, err)
		}
		if flags&flagEndStream != 0 {
			if err := endError(msg); err != nil {
				return fmt.Errorf("cannot start a command in the sandbox %s: %w", p.e.SandboxID, err)
			}
			return errors.New("envd ended the stream before the command started")
		}
		var ev processEvent
		if err := json.Unmarshal(msg, &ev); err != nil {
			return fmt.Errorf("read envd's start event: %w", err)
		}
		if ev.Event.Start != nil {
			p.pid = ev.Event.Start.Pid
			return nil
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
				p.mu.Lock()
				if p.stderr.Len() < 8192 {
					p.stderr.Write(ev.Event.Data.Stderr)
				}
				p.mu.Unlock()
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
func (p *proc) Kill() {
	p.killed.Do(func() {
		p.mu.Lock()
		ended := p.exit != nil
		p.mu.Unlock()
		if !ended {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = p.e.unary(ctx, "SendSignal", signalRequest{Process: selector{Pid: p.pid}, Signal: "SIGNAL_SIGKILL"})
		}
		p.cancel()
		_ = p.stdoutR.CloseWithError(io.ErrClosedPipe)
	})
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
