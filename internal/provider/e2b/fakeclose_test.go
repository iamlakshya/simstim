package e2b

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// shutdownWait is how long the fake envd's shutdown may take before the test
// fails.
const shutdownWait = 10 * time.Second

var (
	errShutDown    = errors.New("the fake envd shut down")
	errBridgeEnded = errors.New("the bridge ended")
)

// running names what the fake envd started and has not seen end: handlers,
// commands and bridges. The shutdown waits for them, and names those left.
type running struct {
	wg    sync.WaitGroup
	mu    sync.Mutex
	next  int
	names map[int]string
}

// add counts one more thing that runs. The returned func says it ended.
func (r *running) add(name string) (ended func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.names == nil {
		r.names = map[int]string{}
	}
	id := r.next
	r.next++
	r.names[id] = name
	r.wg.Add(1)
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			delete(r.names, id)
			r.mu.Unlock()
			r.wg.Done()
		})
	}
}

func (r *running) left() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, name := range r.names {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// onShutdown runs end when the fake shuts down, not when a request drops:
// envd keeps a command running when its request drops. The returned func
// stops it, and says whether it did.
func (f *fakeEnvd) onShutdown(end func()) (stop func() bool) {
	return context.AfterFunc(f.ctx, end)
}

// requestError fails the test for a request that the fake cannot serve: a
// body it cannot read, or input it cannot pass on. A request cut off by the
// client going away, or by the fake's shutdown, has its context ended first,
// and is no fault of the client's.
func (f *fakeEnvd) requestError(r *http.Request, err error) {
	if !f.stopping(r) {
		f.t.Errorf("%s: %v", r.URL.Path, err)
	}
}

// stopping says that the fake shuts down or the request's client went away.
// The shutdown sets f.ctx's error before it ends any request's context, and
// before any onShutdown function runs.
func (f *fakeEnvd) stopping(r *http.Request) bool {
	return f.ctx.Err() != nil || r.Context().Err() != nil
}

// shutdown ends every command, bridge and handler that the fake started, then
// closes its server. Every potentially blocking step, including closing the
// listener and active TLS or HTTP connections, runs inside the f.wait
// deadline. After f.wait, shutdown returns an error that names the blocked
// stage and what is still running. Only the first call does anything.
func (f *fakeEnvd) shutdown() error {
	if !f.closed.CompareAndSwap(false, true) {
		return nil
	}
	budget := f.wait
	if budget <= 0 {
		budget = shutdownWait
	}
	timer := time.NewTimer(budget)
	defer timer.Stop()
	if f.done == nil {
		f.done = make(chan struct{})
	}
	f.stop()
	var stage atomic.Value
	stage.Store("closing the listener and client connections")
	go func() {
		defer close(f.done)
		// Stop taking connections and close every one inside the deadline, so
		// neither a late request nor a blocked TLS close_notify hangs the test.
		_ = f.srv.Config.Close()
		f.srv.CloseClientConnections()
		if f.afterClose != nil {
			stage.Store("running afterClose")
			f.afterClose()
		}
		stage.Store("closing the server")
		f.srv.Close()
		stage.Store("")
		f.run.wg.Wait()
	}()
	select {
	case <-f.done:
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.wrapperErrs) > 0 {
			return fmt.Errorf("a wrapper of the fake envd failed (its report, if any, is above): %s", strings.Join(f.wrapperErrs, "; "))
		}
		return nil
	case <-timer.C:
		left := f.run.left()
		if s, _ := stage.Load().(string); s != "" {
			left = append([]string{s}, left...)
		}
		return fmt.Errorf("the fake envd did not shut down in %v. Still running: %s", budget, strings.Join(left, "; "))
	}
}

func shutdownWithin(t *testing.T, envd *fakeEnvd, limit time.Duration) error {
	t.Helper()
	shut := make(chan error, 1)
	go func() { shut <- envd.shutdown() }()
	select {
	case err := <-shut:
		return err
	case <-time.After(limit):
		t.Fatalf("the shutdown did not end in %v: its own deadline did not hold", limit)
		return nil
	}
}

// short cuts a command to a length that fits in a message.
func short(cmd string) string {
	const n = 60
	if len(cmd) <= n {
		return cmd
	}
	return cmd[:n] + "..."
}

// zeros is input with no end, as a large upload is to a command that is
// killed before it ends.
type zeros struct{}

func (zeros) Read(b []byte) (int, error) {
	clear(b)
	return len(b), nil
}

// shutsDownAfterCancel cancels a command, waits for the client to give up on
// it, and checks that the fake envd then shuts down in time.
func shutsDownAfterCancel(t *testing.T, envd *fakeEnvd, cancel func(), gaveUp <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-gaveUp:
		if err == nil {
			t.Fatal("the cancelled command did not fail")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the client did not give up on the cancelled command in 30s")
	}
	start := time.Now()
	shut := make(chan error, 1)
	go func() { shut <- envd.shutdown() }()
	select {
	case err := <-shut:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * envd.wait):
		t.Fatalf("the fake envd's shutdown did not return in %v", 2*envd.wait)
	}
	t.Logf("the fake envd shut down in %v", time.Since(start).Round(time.Millisecond))
}
