package client_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
)

var fakeID atomic.Int64

// register makes a fresh provider for one test and returns its host.
func register(t *testing.T, lc client.Lifecycle) client.Host {
	t.Helper()
	name := "fake" + string(rune('a'+fakeID.Add(1)))
	client.RegisterProvider(name, func(client.Host) (client.Lifecycle, error) { return lc, nil })
	return client.Host{Name: "box", Machine: &client.Machine{Provider: name, ID: "m-1"}}
}

func TestDialWakesAStoppedMachine(t *testing.T) {
	tr := &clienttest.Transport{}
	m := clienttest.NewMachine(client.MachineStopped, tr)
	h := register(t, m)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := client.Dial(ctx, h, false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	calls := m.Calls()
	// State first, then start, then a check that it answers, then the bridge.
	if len(calls) < 3 || calls[0] != "status" || calls[1] != "start" {
		t.Fatalf("calls = %v, want status then start", calls)
	}
	if _, err := c.Sessions(ctx); err != nil {
		t.Fatalf("the bridge does not answer over the fake transport: %v", err)
	}
	if r := tr.Remotes(); len(r) < 2 || r[0] != "true" || !strings.HasSuffix(r[len(r)-1], " bridge") {
		t.Fatalf("remotes = %q, want a readiness check, then the bridge with no --idle-exit", r)
	}
}

// A bridge that never says hello ends the dial with a user error, and its
// process is killed (#84).
func TestDialGivesUpOnASilentBridge(t *testing.T) {
	defer client.SetHelloWait(50 * time.Millisecond)()
	h := register(t, clienttest.NewMachine(client.MachineRunning, &clienttest.Transport{Silent: true}))
	done := make(chan error, 1)
	go func() {
		_, err := client.Dial(context.Background(), h, false)
		done <- err
	}()
	select {
	case err := <-done:
		var ue *client.UserError
		if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "did not answer") || !strings.Contains(ue.Hint, "host doctor") {
			t.Fatalf("err = %v, want a user error saying the host did not answer", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the dial still waits for the hello")
	}
}

func TestDialDoesNotWakeWithoutWake(t *testing.T) {
	m := clienttest.NewMachine(client.MachineStopped, &clienttest.Transport{})
	h := register(t, m)
	_, err := client.Dial(client.WithoutWake(context.Background()), h, false)
	var ue *client.UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "is stopped") || !strings.Contains(ue.Hint, "host start") {
		t.Fatalf("err = %v, want a user error saying the host is stopped", err)
	}
	if slices.Contains(m.Calls(), "start") {
		t.Fatal("the machine was started")
	}
}

func TestDialMissingMachine(t *testing.T) {
	m := clienttest.NewMachine(client.MachineMissing, &clienttest.Transport{})
	h := register(t, m)
	_, err := client.Dial(context.Background(), h, false)
	var ue *client.UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "is gone") || !strings.Contains(ue.Hint, "host destroy") {
		t.Fatalf("err = %v, want a user error saying the machine is gone", err)
	}
}

func TestDialUnknownProvider(t *testing.T) {
	h := client.Host{Name: "box", Machine: &client.Machine{Provider: "nobody-knows", ID: "x"}}
	_, err := client.Dial(context.Background(), h, false)
	var ue *client.UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "nobody-knows") {
		t.Fatalf("err = %v", err)
	}
}

func TestKeepAliveTransportPingsAndAsksForIdleExit(t *testing.T) {
	tr := &clienttest.Transport{Every: 20 * time.Millisecond}
	m := clienttest.NewMachine(client.MachineRunning, clienttest.KeepAliveTransport{Transport: tr})
	h := register(t, m)
	c, err := client.Dial(context.Background(), h, false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r := tr.Remotes()
	if !strings.HasSuffix(r[len(r)-1], " bridge --idle-exit 30") {
		t.Fatalf("bridge command = %q, want --idle-exit 30", r[len(r)-1])
	}
	deadline := time.Now().Add(5 * time.Second)
	for tr.Pings() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if tr.Pings() < 3 {
		t.Fatalf("pings = %d, want the client to ping by itself", tr.Pings())
	}
}

// leased dials a leased fake machine whose host reports sessions, and
// returns the leases it was given once it has n of them.
func leased(t *testing.T, sessions []client.Session, n int) (*clienttest.Machine, *client.Conn, []time.Duration) {
	t.Helper()
	m := clienttest.NewMachine(client.MachineRunning, &clienttest.Transport{Sessions: sessions})
	m.Busy, m.Idle = time.Hour, 15*time.Minute
	h := register(t, clienttest.LeasedMachine{Machine: m})
	c, err := client.Dial(context.Background(), h, false)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(m.Renewed()) < n && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	return m, c, m.Renewed()
}

func TestLeaseFollowsWork(t *testing.T) {
	defer client.SetLeasePeriod(20 * time.Millisecond)()

	_, c, got := leased(t, []client.Session{{ID: "a", State: api.StateIdle}}, 2)
	c.Close()
	if len(got) < 2 || got[0] != 15*time.Minute || got[1] != 15*time.Minute {
		t.Fatalf("leases with no session working = %v, want 15m each minute", got)
	}

	m, c, got := leased(t, []client.Session{{ID: "a", State: api.StateIdle}, {ID: "b", State: api.StateWorking}}, 2)
	c.Close()
	if len(got) < 2 || got[0] != time.Hour || got[1] != time.Hour {
		t.Fatalf("leases while a session works = %v, want 1h", got)
	}
	// After the client goes, nobody renews it.
	time.Sleep(50 * time.Millisecond)
	n := len(m.Renewed())
	time.Sleep(100 * time.Millisecond)
	if len(m.Renewed()) != n {
		t.Fatalf("the lease was renewed after the connection closed: %v", m.Renewed())
	}
}

func TestTarget(t *testing.T) {
	if got := (client.Host{SSH: "me@box"}).Target(); got != "me@box" {
		t.Fatal(got)
	}
	h := client.Host{Machine: &client.Machine{Provider: "docker", ID: "c1"}}
	if got := h.Target(); got != "docker c1" {
		t.Fatal(got)
	}
}
