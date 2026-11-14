package client

import (
	"context"
	"io"
	"strings"
	"testing"
)

// These objects NEVER run a process or prove SSH/Hello provenance. They are
// explicit generated stamp fixtures for lower source/lifetime unit oracles.
// Main's actual Dial regressions separately exercise the sole production mint.
type hostStateGeneratedTransport struct{ identity byte }

func (*hostStateGeneratedTransport) Start(context.Context, string) (Proc, error) {
	panic("generated state fixture must not start transport")
}
func (*hostStateGeneratedTransport) Run(context.Context, string, io.Reader) ([]byte, error) {
	panic("generated state fixture must not run transport")
}
func (*hostStateGeneratedTransport) ReachHint() string { return "generated fixture" }

type hostStateGeneratedProc struct{ identity byte }
type hostStateGeneratedInput struct{}

func (*hostStateGeneratedInput) Write(data []byte) (int, error) { return len(data), nil }
func (*hostStateGeneratedInput) Close() error                   { return nil }
func (*hostStateGeneratedProc) Stdin() io.WriteCloser           { return &hostStateGeneratedInput{} }
func (*hostStateGeneratedProc) Stdout() io.Reader               { return strings.NewReader("") }
func (*hostStateGeneratedProc) Wait() error                     { return nil }
func (*hostStateGeneratedProc) Kill()                           {}
func (*hostStateGeneratedProc) Stderr() string                  { return "" }

func generatedHostTransportConn(h Host, hostID string, features ...string) *Conn {
	return &Conn{Host: cloneObservationHost(h), hostID: hostID, Features: features, proc: &hostStateGeneratedProc{},
		route: route{t: &hostStateGeneratedTransport{}}, done: make(chan struct{})}
}

func bindGeneratedHostStateFixture(t *testing.T, ctx context.Context, h Host, hostID string) *Conn {
	t.Helper()
	o, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation)
	held, _ := ctx.Value(heldKey{h.Name}).(*heldLock)
	if o == nil || held == nil {
		t.Fatal("generated stamp fixture needs the actual native state/OP scope")
	}
	c := generatedHostTransportConn(h, hostID)
	stamp := &hostTransportStamp{kind: hostTransportSaved, host: o.prospective, source: o.source,
		stateDir: StateDir(), hostsPath: hostsPath(), parent: held.native.parent, op: held.native.reference,
		conn: c, proc: c.proc, transport: c.route.t, hostID: hostID}
	if o.role == hostStateRegistration {
		stamp.kind, stamp.registration = hostTransportRegistration, o
	}
	c.hostTransport = stamp
	if err := c.BindHostStateObservation(ctx); err != nil {
		t.Fatal(err)
	}
	return c
}
