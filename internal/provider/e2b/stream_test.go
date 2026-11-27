package e2b

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/provider"
)

// shortTransit shortens the wait for input on its way to envd, for one test.
func shortTransit(t *testing.T, d time.Duration) {
	t.Helper()
	old := streamTransitWait
	streamTransitWait = d
	t.Cleanup(func() { streamTransitWait = old })
}

// A StreamInput request that ends with an error after part of the input:
// Close says so, and sends no EOF, so the command never takes the part for
// all of its input.
func TestStreamWriterSendsNoEOFAfterACut(t *testing.T) {
	e, envd := envdFor(t, true)
	envd.mu.Lock()
	envd.cutStream = 32 << 10
	envd.mu.Unlock()
	p, err := e.Start(context.Background(), "cat > /dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()
	_, werr := p.Stdin().Write(make([]byte, 1<<20))
	cerr := p.Stdin().Close()
	if cerr == nil || !strings.Contains(cerr.Error(), "the link broke") {
		t.Fatalf("Write = %v, Close = %v, want the cut", werr, cerr)
	}
	if slices.Contains(envd.Paths(), "CloseStdin") {
		t.Fatal("an EOF was sent after a cut input")
	}
}

// envd does not read the request, and keeps it: a Write that waits gives up
// when the transport reads nothing from the pipe for the stall limit. Close
// then sends no EOF.
func TestStreamWriterWriteGivesUpWhenEnvdReadsNothing(t *testing.T) {
	shortStall(t, 300*time.Millisecond)
	e, envd := envdFor(t, true)
	envd.mu.Lock()
	envd.gate = make(chan struct{}) // the request is not read
	envd.mu.Unlock()
	p, err := e.Start(context.Background(), "cat > /dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()
	wrote := make(chan error, 1)
	go func() {
		_, err := p.Stdin().Write(make([]byte, 16<<20)) // more than the buffers hold
		wrote <- err
	}()
	select {
	case err := <-wrote:
		if err == nil || !strings.Contains(err.Error(), "took no input for 300ms") {
			t.Fatalf("Write = %v, want the stall", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Write waited 10s with a stall limit of 300ms")
	}
	if err := p.Stdin().Close(); err == nil {
		t.Fatal("Close = nil after a stalled Write")
	}
	if slices.Contains(envd.Paths(), "CloseStdin") {
		t.Fatal("an EOF was sent after a stalled Write")
	}
}

// envd does not read the request, and keeps it, but the input fits in the
// buffers, so the transport reads the body to its EOF. Close then waits the
// stall limit plus the time for input on its way, and fails with no EOF.
func TestStreamWriterWaitsForEnvdAfterTheLastByte(t *testing.T) {
	shortStall(t, 150*time.Millisecond)
	shortTransit(t, 200*time.Millisecond)
	e, envd := envdFor(t, true)
	envd.mu.Lock()
	envd.gate = make(chan struct{}) // the request is not read
	envd.mu.Unlock()
	p, err := e.Start(context.Background(), "cat > /dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()
	if _, err := p.Stdin().Write(make([]byte, 16<<10)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = p.Stdin().Close()
	if err == nil || !strings.Contains(err.Error(), "did not answer the StreamInput request for 350ms after its last byte") {
		t.Fatalf("Close = %v, want no answer after the last byte", err)
	}
	if took := time.Since(start); took < 350*time.Millisecond {
		t.Fatalf("Close gave up after %v, before the stall limit and the time for input on its way", took)
	}
	if slices.Contains(envd.Paths(), "CloseStdin") {
		t.Fatal("an EOF was sent before envd answered")
	}
}

// A watchdog stopped by a Write that succeeded can never fire later, even when
// its timer has already expired before the goroutine enters select. Stopping
// waits for the watchdog to finish, and the watchdog rechecks the stop state
// before committing a stall.
func TestStreamWriterStoppedWatchdogNeverFires(t *testing.T) {
	e, _ := envdFor(t, true)
	p, err := e.Start(context.Background(), "cat")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()
	got := make(chan []byte, 1)
	go func() {
		out, _ := io.ReadAll(p.Stdout())
		got <- out
	}()
	var live atomic.Int32
	old := watchHook
	t.Cleanup(func() { watchHook = old })
	watchHook = func(stop <-chan struct{}) {
		live.Add(1)
		defer live.Add(-1)
		<-stop
		time.Sleep(3 * time.Millisecond) // past the 1ms stall limit, so t.C is ready too
	}
	sw := p.Stdin().(*streamWriter)
	sw.stall = time.Millisecond
	var want bytes.Buffer
	for i := range 24 {
		line := fmt.Sprintf("part-%d\n", i)
		want.WriteString(line)
		if _, err := p.Stdin().Write([]byte(line)); err != nil {
			t.Fatalf("Write %d = %v, want nil", i, err)
		}
		if n := live.Load(); n != 0 {
			t.Fatalf("Write %d returned with %d stopped watchdogs still running", i, n)
		}
	}
	sw.stall = stallLimit
	watchHook = nil
	time.Sleep(5 * time.Millisecond)
	want.WriteString("after-idle\n")
	if _, err := p.Stdin().Write([]byte("after-idle\n")); err != nil {
		t.Fatalf("Write after idle = %v, want nil", err)
	}
	if err := p.Stdin().Close(); err != nil {
		t.Fatalf("Close = %v, want nil", err)
	}
	if out := <-got; !bytes.Equal(out, want.Bytes()) {
		t.Fatalf("cat gave %d of %d bytes", len(out), want.Len())
	}
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}
}

type pingCountListener struct {
	net.Listener
	stopped *atomic.Int32
	writes  *atomic.Int64
}

func (l pingCountListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return pingCountConn{Conn: c, stopped: l.stopped, writes: l.writes}, nil
}

type pingCountConn struct {
	net.Conn
	stopped *atomic.Int32
	writes  *atomic.Int64
}

func (c pingCountConn) Write(b []byte) (int, error) {
	if c.stopped.Load() > 0 && len(b) > 0 {
		c.writes.Add(1)
	}
	return c.Conn.Write(b)
}

func envdForHTTP2(t *testing.T, customDialer bool) (e *Envd, envd *fakeEnvd, pings, dials *atomic.Int64) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake envd runs /bin/sh")
	}
	t.Setenv("E2B_API_KEY", testKey)
	_, apiSrv := newFakeAPI(t)

	pings = &atomic.Int64{}
	dials = &atomic.Int64{}
	f := &fakeEnvd{
		t:         t,
		wait:      shutdownWait,
		wantHTTP2: true,
		procs:     map[uint32]*fakeCmd{},
		stdin:     map[uint32]io.WriteCloser{},
	}
	f.ctx, f.stop = context.WithCancel(context.Background())
	f.srv = httptest.NewUnstartedServer(f)
	f.srv.EnableHTTP2 = true
	f.srv.Config.BaseContext = func(net.Listener) context.Context { return f.ctx }
	f.srv.Config.HTTP2 = &http.HTTP2Config{
		SendPingTimeout:               10 * time.Millisecond,
		MaxReceiveBufferPerConnection: 4 << 20,
		MaxReceiveBufferPerStream:     256 << 10,
	}
	f.srv.Listener = pingCountListener{Listener: f.srv.Listener, stopped: &f.streamStopped, writes: pings}
	f.srv.StartTLS()
	t.Cleanup(func() {
		if err := f.shutdown(); err != nil {
			t.Error(err)
		}
	})

	hc := f.srv.Client()
	if customDialer {
		pool := x509.NewCertPool()
		pool.AddCert(f.srv.Certificate())
		dialer := &tls.Dialer{
			Config: &tls.Config{
				RootCAs:    pool,
				NextProtos: []string{"h2"},
			},
		}
		tr := &http.Transport{
			ForceAttemptHTTP2: true,
			DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				dials.Add(1)
				return dialer.DialContext(ctx, network, addr)
			},
		}
		t.Cleanup(tr.CloseIdleConnections)
		hc = &http.Client{Transport: tr}
	}

	prov := Provider{APIURL: apiSrv.URL, SandboxURL: f.srv.URL, CLIConfig: filepath.Join(t.TempDir(), "none.json")}
	h, err := prov.Create(context.Background(), provider.Request{Name: "cloud"})
	if err != nil {
		t.Fatal(err)
	}
	h.Machine.Stdin = "stream"
	lc, _ := prov.Open(h)
	if _, err := lc.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	tr, err := lc.Transport(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	e = tr.(*Envd)
	e.HTTP = hc
	return e, f, pings, dials
}

// Over HTTP/2 with TLS, both with a transport that trusts the server's
// certificate and with a custom DialTLSContext that negotiates h2, StreamInput
// uses the client's own transport unchanged. A slow steady reader succeeds,
// while a reader that stops fails within the stall limit even as the server
// keeps sending HTTP/2 PING frames, and sends no CloseStdin after the failure.
func TestStreamWriterOverHTTP2(t *testing.T) {
	for _, tc := range []struct {
		name   string
		custom bool
	}{
		{"trusted cert", false},
		{"custom DialTLSContext", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, envd, pings, dials := envdForHTTP2(t, tc.custom)
			reset := func(rate, stop int) {
				waitFor(t, "previous stopped stream", func() bool {
					return envd.streamStopped.Load() == 0
				})
				pings.Store(0)
				envd.mu.Lock()
				envd.paths = nil
				envd.streamRate = rate
				envd.stopStream = stop
				envd.mu.Unlock()
			}

			t.Run("steady reader", func(t *testing.T) {
				shortStall(t, 1500*time.Millisecond)
				reset(4<<20, 0) // exceeds the 256 KiB HTTP/2 stream window, paced at 4 MiB/s
				p, err := e.Start(context.Background(), "wc -c")
				if err != nil {
					t.Fatal(err)
				}
				defer p.Kill()
				outCh := make(chan []byte, 1)
				go func() {
					out, _ := io.ReadAll(p.Stdout())
					outCh <- out
				}()
				in := bytes.Repeat([]byte("x"), 256<<10)
				if _, err := p.Stdin().Write(in); err != nil {
					t.Fatalf("Write = %v", err)
				}
				if err := p.Stdin().Close(); err != nil {
					t.Fatalf("Close = %v", err)
				}
				out := <-outCh
				if err := p.Wait(); err != nil || strings.TrimSpace(string(out)) != fmt.Sprint(len(in)) {
					t.Fatalf("wc = %q, %v, want %d", out, err, len(in))
				}
				if !slices.Contains(envd.Paths(), "CloseStdin") {
					t.Fatal("CloseStdin was not sent after a complete stream")
				}
			})

			t.Run("stopped reader during Write with PINGs", func(t *testing.T) {
				reset(0, 16<<10)
				p, err := e.Start(context.Background(), "cat > /dev/null")
				if err != nil {
					t.Fatal(err)
				}
				defer p.Kill()
				if _, err := p.Stdin().Write(make([]byte, 2*chunk)); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "stopped stream", func() bool {
					return envd.streamStopped.Load() > 0
				})
				pings.Store(0)
				p.Stdin().(*streamWriter).stall = 220 * time.Millisecond
				start := time.Now()
				wrote := make(chan error, 1)
				go func() {
					_, err := p.Stdin().Write(make([]byte, 2<<20)) // exhausts the 256 KiB HTTP/2 window
					wrote <- err
				}()
				select {
				case err := <-wrote:
					if err == nil || !strings.Contains(err.Error(), "took no input for 220ms") {
						t.Fatalf("Write = %v, want the stall", err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("Write waited 10s over HTTP/2 with a stall limit of 220ms")
				}
				if took := time.Since(start); took > 5*time.Second {
					t.Fatalf("Write failed after %v, with a stall limit of 220ms", took)
				}
				if got := pings.Load(); got < 2 {
					t.Fatalf("server writes while stopped = %d, want HTTP/2 PINGs during the stall", got)
				}
				if err := p.Stdin().Close(); err == nil {
					t.Fatal("Close = nil after a stalled HTTP/2 Write")
				}
				if slices.Contains(envd.Paths(), "CloseStdin") {
					t.Fatal("CloseStdin was sent after a failed HTTP/2 stream")
				}
			})

			t.Run("stopped reader during Close with PINGs", func(t *testing.T) {
				shortTransit(t, 60*time.Millisecond)
				reset(0, 16<<10)
				p, err := e.Start(context.Background(), "cat > /dev/null")
				if err != nil {
					t.Fatal(err)
				}
				defer p.Kill()
				if _, err := p.Stdin().Write(make([]byte, 2*chunk)); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "stopped stream", func() bool {
					return envd.streamStopped.Load() > 0
				})
				pings.Store(0)
				p.Stdin().(*streamWriter).stall = 160 * time.Millisecond
				start := time.Now()
				closed := make(chan error, 1)
				go func() { closed <- p.Stdin().Close() }()
				select {
				case err := <-closed:
					if err == nil || !strings.Contains(err.Error(), "did not answer the StreamInput request for 220ms after its last byte") {
						t.Fatalf("Close = %v, want no answer after the last byte", err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("Close waited 10s over HTTP/2 with a 220ms bound")
				}
				if took := time.Since(start); took < 220*time.Millisecond || took > 5*time.Second {
					t.Fatalf("Close took %v, want between 220ms and 5s", took)
				}
				if got := pings.Load(); got < 2 {
					t.Fatalf("server writes while stopped = %d, want HTTP/2 PINGs during the stall", got)
				}
				if slices.Contains(envd.Paths(), "CloseStdin") {
					t.Fatal("CloseStdin was sent after a stalled HTTP/2 Close")
				}
			})

			if tc.custom && dials.Load() != 1 {
				t.Fatalf("DialTLSContext dials = %d, want 1 shared HTTP/2 connection", dials.Load())
			}
		})
	}
}
