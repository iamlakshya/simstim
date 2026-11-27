package clienttest

import (
	"context"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
)

// Machine is a fake machine: its state lives in memory, and every call is
// recorded.
type Machine struct {
	T client.Transport

	mu      sync.Mutex
	state   string
	calls   []string
	renewed []time.Duration
	// Busy and Idle are the leases, for LeasedMachine.
	Busy, Idle time.Duration
	// DestroyErr, if set, makes Destroy fail with it, and keep the
	// machine.
	DestroyErr error
	// StatusErr, if set, makes Status fail with it, as a provider that
	// cannot tell the machine's state does.
	StatusErr error
}

// NewMachine returns a machine in the given state.
func NewMachine(state string, t client.Transport) *Machine {
	return &Machine{state: state, T: t}
}

func (m *Machine) call(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, name)
}

// Calls lists the lifecycle calls so far, in order.
func (m *Machine) Calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.calls...)
}

// Renewed lists the leases asked for.
func (m *Machine) Renewed() []time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]time.Duration(nil), m.renewed...)
}

// SetState sets the state that Status reports.
func (m *Machine) SetState(s string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = s
}

// Status reports the state, or fails with StatusErr.
func (m *Machine) Status(context.Context) (string, error) {
	m.call("status")
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.StatusErr != nil {
		return "", m.StatusErr
	}
	return m.state, nil
}

// Start makes it running.
func (m *Machine) Start(context.Context) error {
	m.call("start")
	m.SetState(client.MachineRunning)
	return nil
}

// Stop makes it stopped.
func (m *Machine) Stop(context.Context) error {
	m.call("stop")
	m.SetState(client.MachineStopped)
	return nil
}

// Destroy makes it missing, or fails with DestroyErr.
func (m *Machine) Destroy(context.Context) error {
	m.call("destroy")
	if m.DestroyErr != nil {
		return m.DestroyErr
	}
	m.SetState(client.MachineMissing)
	return nil
}

// Transport returns T.
func (m *Machine) Transport(context.Context, bool) (client.Transport, error) {
	m.call("transport")
	return m.T, nil
}

// LeasedMachine is a Machine that is also a client.Leaser.
type LeasedMachine struct{ *Machine }

// Renew records the lease.
func (l LeasedMachine) Renew(_ context.Context, d time.Duration) error {
	l.call("renew")
	l.mu.Lock()
	defer l.mu.Unlock()
	l.renewed = append(l.renewed, d)
	return nil
}

// Leases are Busy and Idle.
func (l LeasedMachine) Leases() (busy, idle time.Duration) { return l.Busy, l.Idle }
