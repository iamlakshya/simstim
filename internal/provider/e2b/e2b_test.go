package e2b

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
	"github.com/iamlakshya/simstim/internal/provider"
)

// testProvider points the provider at the fakes, with the key in the
// environment and no CLI login.
func testProvider(t *testing.T) (Provider, *fakeAPI, *fakeEnvd) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake envd runs /bin/sh")
	}
	t.Setenv("E2B_API_KEY", testKey)
	api, apiSrv := newFakeAPI(t)
	envd, envdSrv := newFakeEnvd(t)
	return Provider{APIURL: apiSrv.URL, SandboxURL: envdSrv.URL, CLIConfig: filepath.Join(t.TempDir(), "none.json")}, api, envd
}

func TestCreateSendsTheRecordedShape(t *testing.T) {
	p, api, _ := testProvider(t)
	var out bytes.Buffer
	h, err := p.Create(context.Background(), provider.Request{Name: "cloud", Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	if h.Machine.Provider != "e2b" || h.Machine.ID != testID || h.Machine.Template != DefaultTemplate || h.Machine.User != "user" || h.SSH != "" {
		t.Fatalf("host = %+v, machine = %+v", h, h.Machine)
	}
	calls := api.Calls()
	if len(calls) != 2 || calls[0].Path != "/templates" || calls[1].Path != "/v2/sandboxes" {
		t.Fatalf("calls = %+v", calls)
	}
	var want map[string]any
	if err := json.Unmarshal(entry(t, fixtures(t), "create").Request, &want); err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(calls[1].Body)
	wantJSON, _ := json.Marshal(want)
	if string(got) != string(wantJSON) {
		t.Fatalf("create body\n got %s\nwant %s", got, wantJSON)
	}
	if !strings.Contains(out.String(), testID) {
		t.Fatalf("create did not name the sandbox: %q", out.String())
	}
}

func TestTemplateCheck(t *testing.T) {
	p, _, _ := testProvider(t)
	_, err := p.Create(context.Background(), provider.Request{Name: "x", Template: "missing"})
	var ue *client.UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "no template") || !strings.Contains(ue.Hint, "e2b template create missing --cpu-count 2 --memory-mb 4096") {
		t.Fatalf("err = %v", err)
	}
	var out bytes.Buffer
	if _, err := p.Create(context.Background(), provider.Request{Name: "x", Template: "tiny", Out: &out}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "has 1 vCPUs and 512 MiB") {
		t.Fatalf("no note about a small template: %q", out.String())
	}
}

func TestLifecycle(t *testing.T) {
	p, api, _ := testProvider(t)
	ctx := context.Background()
	h, err := p.Create(ctx, provider.Request{Name: "cloud"})
	if err != nil {
		t.Fatal(err)
	}
	h.Machine.IdleStop, h.Machine.BusyLease = "20m", "90m"
	lc, _ := p.Open(h)
	status := func() string {
		s, err := lc.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if status() != client.MachineRunning {
		t.Fatal(status())
	}
	if err := lc.Stop(ctx); err != nil || status() != client.MachineStopped {
		t.Fatalf("pause: %v, %s", err, status())
	}
	if err := lc.Start(ctx); err != nil || status() != client.MachineRunning {
		t.Fatalf("resume: %v, %s", err, status())
	}
	l := lc.(client.Leaser)
	if busy, idle := l.Leases(); busy != 90*time.Minute || idle != 20*time.Minute {
		t.Fatalf("leases = %v, %v", busy, idle)
	}
	if err := l.Renew(ctx, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := lc.Destroy(ctx); err != nil || status() != client.MachineMissing {
		t.Fatalf("kill: %v, %s", err, status())
	}
	var connect, timeout map[string]any
	for _, c := range api.Calls() {
		switch {
		case strings.HasSuffix(c.Path, "/connect"):
			connect = c.Body
		case strings.HasSuffix(c.Path, "/timeout"):
			timeout = c.Body
		}
	}
	if connect["timeout"] != float64(20*60) {
		t.Fatalf("connect body = %v, want the idle lease", connect)
	}
	if timeout["timeout"] != float64(3600) {
		t.Fatalf("timeout body = %v", timeout)
	}
}

func TestLeaseDefaults(t *testing.T) {
	b := &box{h: client.Host{Machine: &client.Machine{}}}
	if busy, idle := b.Leases(); busy != DefaultBusyLease || idle != DefaultIdleLease {
		t.Fatalf("%v %v", busy, idle)
	}
	b.h.Machine.IdleStop = "never"
	if busy, idle := b.Leases(); idle != busy {
		t.Fatalf("never: idle = %v, want the busy lease %v", idle, busy)
	}
}

func TestCredentials(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the token command is a POSIX shell command")
	}
	ctx := context.Background()
	dir := t.TempDir()
	v2 := filepath.Join(dir, "v2.json")
	v1 := filepath.Join(dir, "v1.json")
	_ = os.WriteFile(v2, []byte(`{"version":2,"projectApiKey":"from-cli-v2","tokens":{"access_token":"x"}}`), 0o600)
	_ = os.WriteFile(v1, []byte(`{"version":1,"teamApiKey":"from-cli-v1"}`), 0o600)
	before, _ := os.ReadFile(v2)

	t.Setenv("E2B_API_KEY", "from-env")
	if k, _ := apiKey(ctx, "echo from-command", v2); k != "from-env" {
		t.Fatalf("the environment comes first, got %q", k)
	}
	t.Setenv("E2B_API_KEY", "")
	if k, _ := apiKey(ctx, "echo from-command", v2); k != "from-cli-v2" {
		t.Fatalf("then the CLI's login, got %q", k)
	}
	if k, _ := apiKey(ctx, "", v1); k != "from-cli-v1" {
		t.Fatalf("an old CLI login, got %q", k)
	}
	if k, _ := apiKey(ctx, "echo ' from-command '", filepath.Join(dir, "none")); k != "from-command" {
		t.Fatalf("then the token command, got %q", k)
	}
	if after, _ := os.ReadFile(v2); !bytes.Equal(before, after) {
		t.Fatal("the CLI's login file was changed")
	}
	_, err := apiKey(ctx, "", filepath.Join(dir, "none"))
	var ue *client.UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Hint, "E2B_API_KEY") {
		t.Fatalf("no key: %v", err)
	}
	_, err = apiKey(ctx, "echo secret-looking; echo bad >&2; exit 1", filepath.Join(dir, "none"))
	if !errors.As(err, &ue) || strings.Contains(err.Error(), "secret-looking") {
		t.Fatalf("a failing token command must not show what it printed: %v", err)
	}
}

func TestAPIErrors(t *testing.T) {
	p, _, _ := testProvider(t)
	t.Setenv("E2B_API_KEY", "wrong")
	_, err := p.Create(context.Background(), provider.Request{Name: "x"})
	var ae *apiError
	if !errors.As(err, &ae) || ae.Status != 401 || strings.Contains(err.Error(), "wrong") {
		t.Fatalf("err = %v", err)
	}
}

// envdFor returns a transport to the fake envd for a created sandbox.
func envdFor(t *testing.T, stream bool) (*Envd, *fakeEnvd) {
	t.Helper()
	p, _, envd := testProvider(t)
	h, err := p.Create(context.Background(), provider.Request{Name: "cloud"})
	if err != nil {
		t.Fatal(err)
	}
	if stream {
		h.Machine.Stdin = "stream"
	}
	lc, _ := p.Open(h)
	if _, err := lc.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	tr, err := lc.Transport(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	e := tr.(*Envd)
	if e.KeepAlive() <= 0 {
		t.Fatal("the envd transport must ask the client to ping")
	}
	return e, envd
}

func TestEnvdRoundTripSendInput(t *testing.T) {
	e, envd := envdFor(t, false)
	clienttest.RoundTrip(t, e)
	paths := strings.Join(envd.Paths(), " ")
	if !strings.Contains(paths, "SendInput") || !strings.Contains(paths, "CloseStdin") || strings.Contains(paths, "StreamInput") {
		t.Fatalf("calls = %s", paths)
	}
}

func TestEnvdRoundTripStreamInput(t *testing.T) {
	e, envd := envdFor(t, true)
	clienttest.RoundTrip(t, e)
	paths := strings.Join(envd.Paths(), " ")
	if !strings.Contains(paths, "StreamInput") || strings.Contains(paths, "SendInput") {
		t.Fatalf("calls = %s", paths)
	}
}

func TestEnvdRun(t *testing.T) {
	e, _ := envdFor(t, false)
	out, err := e.Run(context.Background(), "tr a-z A-Z", strings.NewReader("hello"))
	if err != nil || string(out) != "HELLO" {
		t.Fatalf("run = %q, %v", out, err)
	}
	_, err = e.Run(context.Background(), "echo oops >&2; exit 3", nil)
	if err == nil || !strings.Contains(err.Error(), "exit status 3") || !strings.Contains(err.Error(), "oops") {
		t.Fatalf("a failing command: %v", err)
	}
	// What the command writes to stderr is the host's text: the error keeps
	// it cleaned, ESC c and OSC 52 and hidden characters gone.
	_, err = e.Run(context.Background(), `printf 'a\033c b\033]52;c;aGk=\007 c\342\200\256d\342\200\213e\n' >&2; exit 3`, nil)
	if err == nil || !strings.Contains(err.Error(), "ac b]52;c;aGk= cde") || strings.ContainsAny(err.Error(), "\x1b\x07\u202e\u200b") {
		t.Fatalf("a failing command with a hostile stderr: %q", err)
	}
	// The helper is not installed: the shell's "not found" reaches stderr,
	// which is how the client knows to install it.
	p, err := e.Start(context.Background(), "/nowhere/helper bridge")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(p.Stdout())
	if err := p.Wait(); err == nil || !strings.Contains(p.Stderr(), "not found") && !strings.Contains(p.Stderr(), "No such file") {
		t.Fatalf("wait = %v, stderr = %q", err, p.Stderr())
	}
}

func TestEnvdKill(t *testing.T) {
	e, envd := envdFor(t, false)
	p, err := e.Start(context.Background(), "sleep 30")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, _ = io.ReadAll(p.Stdout()); done <- p.Wait() }()
	p.Kill()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Kill did not end the command")
	}
	envd.mu.Lock()
	defer envd.mu.Unlock()
	if len(envd.sigs) != 1 || envd.sigs[0] != "SIGNAL_SIGKILL" {
		t.Fatalf("signals = %v", envd.sigs)
	}
}

func TestEnvdDroppedStreamIsUnreachable(t *testing.T) {
	e, envd := envdFor(t, false)
	envd.mu.Lock()
	envd.drop = true
	envd.mu.Unlock()
	p, err := e.Start(context.Background(), "sleep 30")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(p.Stdout())
	if err := p.Wait(); !errors.Is(err, client.ErrUnreachable) {
		t.Fatalf("wait = %v, want ErrUnreachable", err)
	}
	p.Kill()
}

func TestEnvelopes(t *testing.T) {
	var buf bytes.Buffer
	_ = writeEnvelope(&buf, 0, []byte(`{"a":1}`))
	_ = writeEnvelope(&buf, flagEndStream, []byte(`{"error":{"code":"unauthenticated","message":"no"}}`))
	f, m, err := readEnvelope(&buf)
	if err != nil || f != 0 || string(m) != `{"a":1}` {
		t.Fatalf("%d %q %v", f, m, err)
	}
	f, m, err = readEnvelope(&buf)
	if err != nil || f != flagEndStream {
		t.Fatal(f, err)
	}
	if err := endError(m); err == nil || !strings.Contains(err.Error(), "unauthenticated") {
		t.Fatalf("end error = %v", err)
	}
	if _, _, err := readEnvelope(&buf); !errors.Is(err, io.EOF) {
		t.Fatalf("after the last envelope: %v", err)
	}
}

// The recorded envd shapes decode into the client's types.
func TestEnvdFixture(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "envd.json"))
	if err != nil {
		t.Fatal(err)
	}
	b = bytes.ReplaceAll(b, []byte("{{brand}}"), []byte(brand.Name))
	var fx struct {
		Start struct {
			Request startRequest      `json:"request"`
			Events  []json.RawMessage `json:"events"`
			EndErr  json.RawMessage   `json:"end_stream_error"`
		} `json:"start"`
		SendInput struct {
			Request sendInputRequest `json:"request"`
		} `json:"send_input"`
	}
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	if fx.Start.Request.Process.Args[1] != "cat" || !fx.Start.Request.Stdin {
		t.Fatalf("%+v", fx.Start.Request)
	}
	var evs []processEvent
	for _, raw := range fx.Start.Events {
		var ev processEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			t.Fatal(err)
		}
		evs = append(evs, ev)
	}
	if evs[0].Event.Start.Pid != 4242 || !bytes.Equal(evs[2].Event.Data.Stdout, []byte{0, 1, 2, 0xff}) ||
		string(evs[3].Event.Data.Stderr) != "warning\n" || evs[4].Event.End.ExitCode != 3 {
		t.Fatalf("events = %+v", evs)
	}
	if err := endError(fx.Start.EndErr); err == nil || !strings.Contains(err.Error(), "unauthenticated") {
		t.Fatal(err)
	}
	if string(fx.SendInput.Request.Input.Stdin) != "hello\n" || fx.SendInput.Request.Process.Pid != 4242 {
		t.Fatalf("%+v", fx.SendInput.Request)
	}
}
