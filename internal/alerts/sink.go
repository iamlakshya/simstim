package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/version"
)

// Delivery timings.
const (
	httpTimeout    = 10 * time.Second // one HTTP attempt
	deliverTimeout = 25 * time.Second // one destination, with its retry
	retryWait      = 2 * time.Second
	maxOutput      = 1024 // bytes of a command's output kept for the log
)

// commandTimeout is the longest a command destination may run. Tests shorten it.
var commandTimeout = 10 * time.Second

// sink delivers events to one destination.
type sink interface {
	send(ctx context.Context, ev Event) (output string, err error)
}

// sinkEnv is what sinks need from the host.
type sinkEnv struct {
	env    []string // the environment of commands
	dir    string   // the working folder of commands
	client *http.Client
}

// newSink makes the sink for a checked destination.
func newSink(d api.AlertDestination, e sinkEnv) sink {
	switch d.Type {
	case "command":
		return &commandSink{run: d.Run, env: e.env, dir: e.dir}
	case "ntfy":
		return &ntfySink{d: d, client: e.client}
	default:
		return &webhookSink{d: d, client: e.client}
	}
}

// Target describes a destination for logs and results, with the secret parts of an
// address hidden. For a command, it is the start of the command line.
func Target(d api.AlertDestination) string {
	if d.Type == "command" {
		run := strings.Join(strings.Fields(d.Run), " ")
		if len(run) > 60 {
			run = run[:57] + "…"
		}
		return run
	}
	return RedactURL(d.URL)
}

// NewHTTPClient returns the client the HTTP sinks use: a 10 s timeout, and
// redirects followed only within the same host and never down to http.
func NewHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	return &http.Client{
		Timeout:   httpTimeout,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			first := via[0].URL
			if len(via) >= 3 || req.URL.Host != first.Host || (first.Scheme == "https" && req.URL.Scheme != "https") {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

// statusError is an HTTP answer other than 2xx.
type statusError struct {
	code int
	body string
}

func (e *statusError) Error() string {
	msg := fmt.Sprintf("HTTP %d", e.code)
	if e.code >= 300 && e.code < 400 {
		msg += " (a redirect to another host is not followed)"
	}
	if e.body != "" {
		msg += ": " + e.body
	}
	return msg
}

// netError is a failure to reach a destination. It never holds the address,
// which can be a secret.
type netError struct{ err error }

func (e *netError) Error() string { return "cannot reach it: " + e.err.Error() }
func (e *netError) Unwrap() error { return e.err }

// retryable reports whether a second try could succeed: a network error or a
// 5xx answer.
func retryable(err error) bool {
	var se *statusError
	if errors.As(err, &se) {
		return se.code >= 500
	}
	var ne *netError
	return errors.As(err, &ne)
}

// deliver sends ev to s, and tries once more after wait if the first try met
// a network error or a 5xx answer.
func deliver(ctx context.Context, s sink, ev Event, wait time.Duration) (string, error) {
	out, err := s.send(ctx, ev)
	if err == nil || !retryable(err) {
		return out, err
	}
	select {
	case <-time.After(wait):
	case <-ctx.Done():
		return out, err
	}
	return s.send(ctx, ev)
}

// post sends one HTTP request and checks the answer.
func post(client *http.Client, req *http.Request) error {
	req.Header.Set("User-Agent", brand.HostBinary+"/"+version.Version)
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // url.Error's text holds the address, which can be a secret
		}
		return &netError{err: err}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 == 2 {
		return nil
	}
	return &statusError{code: resp.StatusCode, body: oneLine(string(body), 200)}
}

// oneLine makes s fit on one log line of at most n bytes.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

// headerText makes s safe for an HTTP header: one line, and RFC 2047 encoded
// when it is not ASCII.
func headerText(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	return mime.QEncoding.Encode("utf-8", s)
}

// ntfySink posts to an ntfy topic.
type ntfySink struct {
	d      api.AlertDestination
	client *http.Client
}

var ntfyTags = map[string]string{
	NeedsInput: "bell", TurnDone: "white_check_mark", Exited: "checkered_flag", Failed: "x",
	OOM: "warning", Memory: "chart_with_upwards_trend", Started: "arrow_forward", Restored: "arrows_counterclockwise",
	Test: "test_tube",
}

func (n *ntfySink) send(ctx context.Context, ev Event) (string, error) {
	token := n.d.Token
	if n.d.TokenFile != "" {
		t, err := readToken(n.d.TokenFile)
		if err != nil {
			return "", err
		}
		token = t
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.d.URL, strings.NewReader(ev.Text()))
	if err != nil {
		return "", errors.New("the ntfy url is not valid")
	}
	req.Header.Set("Title", headerText(brand.DisplayName+" · "+ev.Host))
	req.Header.Set("Priority", n.priority(ev.Event))
	req.Header.Set("Tags", ntfyTags[ev.Event])
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return "", post(n.client, req)
}

func (n *ntfySink) priority(kind string) string {
	if n.d.Priority != "" {
		return strings.ToLower(n.d.Priority)
	}
	switch kind {
	case NeedsInput, Failed, OOM, Memory:
		return "high"
	}
	return "default"
}

// webhookSink posts JSON to a web address: the event itself, or a message
// shaped for Slack or Discord.
type webhookSink struct {
	d      api.AlertDestination
	client *http.Client
}

func (w *webhookSink) send(ctx context.Context, ev Event) (string, error) {
	var body []byte
	switch w.d.Format {
	case "slack":
		body, _ = json.Marshal(map[string]string{"text": ev.Text()})
	case "discord":
		text := ev.Text()
		if len(text) > 1900 {
			text = text[:1900] + "…"
		}
		body, _ = json.Marshal(map[string]string{"content": text})
	default:
		body = ev.JSON()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.d.URL, bytes.NewReader(body))
	if err != nil {
		return "", errors.New("the webhook url is not valid")
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range w.d.Headers {
		req.Header.Set(k, v)
	}
	return "", post(w.client, req)
}

// commandSink runs a command line with sh -c. The event reaches it only on
// stdin, as JSON, and in environment variables: never in the command line.
type commandSink struct {
	run string
	env []string
	dir string
}

func (c *commandSink) send(ctx context.Context, ev Event) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-c", c.run)
	cmd.Env = append(append([]string(nil), c.env...), ev.env()...)
	cmd.Dir = c.dir
	cmd.Stdin = bytes.NewReader(append(ev.JSON(), '\n'))
	out := &capped{max: maxOutput}
	cmd.Stdout, cmd.Stderr = out, out
	ownGroup(cmd)
	err := cmd.Run()
	text := strings.TrimSpace(out.String())
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return text, fmt.Errorf("the command took longer than %s and was stopped", commandTimeout)
	case errors.Is(err, exec.ErrWaitDelay):
		// The command succeeded, but left a child holding its output open.
		return text, nil
	case err != nil:
		return text, fmt.Errorf("the command failed: %w", err)
	}
	return text, nil
}

// capped keeps the first max bytes written to it and drops the rest, so a
// chatty command can neither fill memory nor block on a full pipe.
type capped struct {
	max int
	buf bytes.Buffer
	cut bool
}

func (c *capped) Write(p []byte) (int, error) {
	n := len(p)
	if room := max(c.max-c.buf.Len(), 0); n > room {
		c.cut = true
		p = p[:room]
	}
	c.buf.Write(p)
	return n, nil
}

func (c *capped) String() string {
	if c.cut {
		return c.buf.String() + "…"
	}
	return c.buf.String()
}
