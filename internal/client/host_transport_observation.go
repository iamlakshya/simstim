package client

import (
	"context"
	"errors"
	"os"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

type hostTransportKind uint8

const (
	hostTransportSaved hostTransportKind = iota + 1
	hostTransportRegistration
	hostTransportProvisional
)

type hostTransportAttemptKey struct{}

// A successful saved stamp outlives its short Dial OP, but never its original
// membership Source. Registration keeps its original stage/OP owner instead.
type hostTransportStamp struct {
	kind         hostTransportKind
	host         Host
	source       hostStateSource
	stateDir     string
	hostsPath    string
	parent       cachefs.Reference
	op           cachefs.Reference
	registration *HostStateObservation
	conn         *Conn
	proc         Proc
	transport    Transport
	hostID       string
	priorID      string
	idChanged    bool
	agentOnly    bool
	features     []string
}

type hostTransportAttempt struct {
	kind        hostTransportKind
	host        Host
	observation *HostStateObservation
	op          *heldLock
	priorID     string
	active      atomic.Bool
	finished    atomic.Bool
}

func beginHostTransportObservation(ctx context.Context, h Host) (context.Context, func(), *hostTransportAttempt, error) {
	if err := ctx.Err(); err != nil {
		return ctx, nil, nil, err
	}
	if existing, _ := ctx.Value(hostTransportAttemptKey{}).(*hostTransportAttempt); existing != nil {
		if err := checkHostTransportAttempt(ctx, existing, h); err != nil {
			return ctx, nil, nil, err
		}
		return ctx, func() {}, existing, nil
	}
	if !ValidHostName(h.Name) ||
		ctx.Value(hostMembershipContextKey{}) != nil || ctx.Value(hostMutationContextKey{}) != nil ||
		ctx.Value(hostForgetMembershipKey{}) != nil || ctx.Value(hostStateInspectionKey{}) != nil || ctx.Value(hostTerminalInspectionKey{}) != nil {
		return ctx, nil, nil, hostStateRemovalError("transport observation cannot begin inside another transport or local state scope")
	}
	attempt := &hostTransportAttempt{}
	owned := ctx
	unlock := func() {}
	o, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation)
	if o == nil {
		saved, err := SavedEntry(h.Name)
		if err != nil {
			return ctx, nil, nil, err
		}
		if saved == nil {
			// An unobserved unsaved SDK connection may perform ordinary reads.
			// It does not fabricate a stage, OP, membership Source or write role.
			attempt.kind = hostTransportProvisional
			attempt.host = cloneObservationHost(h)
			attempt.active.Store(true)
			bound := context.WithValue(ctx, hostTransportAttemptKey{}, attempt)
			return bound, sync.OnceFunc(func() { attempt.active.Store(false) }), attempt, nil
		}
		if !SameEntry(*saved, h) {
			return ctx, nil, nil, grantIdentityError(h)
		}
		owned, unlock, err = LockHost(ctx, h.Name, "connect")
		if err != nil {
			return ctx, nil, nil, err
		}
		o, err = ObserveHostStateContext(owned, h, "")
		if err != nil {
			unlock()
			return ctx, nil, nil, err
		}
		owned = WithHostStateObservation(owned, o)
	}
	captured, err := captureHostStateObservation(owned, h)
	if err != nil {
		unlock()
		return ctx, nil, nil, err
	}
	attempt.observation = captured
	// Share the same private immutable facts, not additional whole-Host copies.
	attempt.host = captured.prospective
	held, _ := owned.Value(heldKey{h.Name}).(*heldLock)
	if held == nil || held.terminal.Load() {
		unlock()
		return ctx, nil, nil, hostStateRemovalError("the transport lost its original actual OP")
	}
	startup, leave, err := held.stateLifetime().enterTransport(owned)
	if err != nil {
		unlock()
		return ctx, nil, nil, err
	}
	switch captured.role {
	case hostStateSaved:
		attempt.kind = hostTransportSaved
		attempt.priorID, err = transportPriorHostID(startup, captured, h)
	case hostStateRegistration:
		attempt.kind = hostTransportRegistration
		attempt.priorID, err = transportPriorHostID(startup, captured, h)
	default:
		err = hostStateRemovalError("this local observation has no transport capture role")
	}
	if err == nil {
		err = withHostMembershipContext(startup, captured.checkMembership)
	}
	if err != nil {
		leave()
		unlock()
		return ctx, nil, nil, err
	}
	attempt.op = held
	attempt.active.Store(true)
	bound := context.WithValue(startup, hostTransportAttemptKey{}, attempt)
	cleanup := sync.OnceFunc(func() {
		attempt.active.Store(false)
		leave()
		unlock()
	})
	return bound, cleanup, attempt, nil
}

// finishHostTransportObservation is called only in actual dial AFTER accepted
// Hello.Check and successful H4 noteHostID, before exposing that same Conn.
// The call-site proof owns authenticated transport/pin/Hello provenance.
func finishHostTransportObservation(ctx context.Context, attempt *hostTransportAttempt, c *Conn) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c != nil {
		if err := checkHostTransportAttempt(ctx, attempt, c.Host); err != nil {
			return err
		}
	}
	if attempt == nil || !attempt.active.Load() || ctx.Value(hostTransportAttemptKey{}) != attempt ||
		c == nil || c.hostTransport != nil || !SameEntry(attempt.host, c.Host) ||
		c.proc == nil || c.route.t == nil ||
		c.HostID() != "" && !ValidHostID(c.HostID()) || !liveHostTransportConn(c) {
		return hostStateRemovalError("the accepted hello has no exact original transport attempt")
	}
	stamp := &hostTransportStamp{kind: attempt.kind, host: attempt.host, stateDir: StateDir(), hostsPath: hostsPath(),
		conn: c, proc: c.proc, transport: c.route.t, hostID: c.HostID(), priorID: attempt.priorID, idChanged: c.hostIDChanged, agentOnly: c.agentOnly, features: slices.Clone(c.Features)}
	if attempt.kind != hostTransportProvisional {
		o := attempt.observation
		held, _ := ctx.Value(heldKey{attempt.host.Name}).(*heldLock)
		if held == nil || held != attempt.op || held.terminal.Load() || o == nil {
			return hostStateRemovalError("the accepted hello changed its original physical OP")
		}
		if err := withHostMembershipContext(ctx, o.checkMembership); err != nil {
			return err
		}
		stamp.source, stamp.parent, stamp.op = o.source, held.native.parent, held.native.reference
		if attempt.kind == hostTransportRegistration {
			if o.role != hostStateRegistration || o.capturedOP.Load() != held {
				return hostStateRemovalError("the registration hello lost its original stage or captured OP")
			}
			stamp.registration = o
		}
	}
	if !attempt.finished.CompareAndSwap(false, true) {
		return hostStateRemovalError("the transport attempt already bound an accepted hello")
	}
	c.hostTransport = stamp
	return nil
}

// Exact interface identity is checked only when Go can compare the actual
// dynamic object. No raw FD, native identity parser or equality guess is used.
func sameHostRuntimeObject(a, b any) bool {
	if a == nil || b == nil || reflect.TypeOf(a) != reflect.TypeOf(b) || !reflect.ValueOf(a).Comparable() || !reflect.ValueOf(b).Comparable() {
		return false
	}
	value := reflect.ValueOf(a)
	if (value.Kind() == reflect.Pointer || value.Kind() == reflect.Chan) && value.IsNil() {
		return false
	}
	return a == b
}

func liveHostTransportConn(c *Conn) bool {
	if c == nil || c.done == nil {
		return false
	}
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

func (s *hostTransportStamp) checkConn(c *Conn) error {
	if s == nil || c == nil || c.hostTransport != s || s.conn != c || s.stateDir != StateDir() || s.hostsPath != hostsPath() ||
		!SameEntry(s.host, c.Host) || s.hostID != c.HostID() || s.idChanged != c.hostIDChanged || s.agentOnly != c.agentOnly || !slices.Equal(s.features, c.Features) ||
		!sameHostRuntimeObject(s.proc, c.proc) || !sameHostRuntimeObject(s.transport, c.route.t) || !liveHostTransportConn(c) {
		return hostStateRemovalError("the connection differs from its original accepted transport observation")
	}
	return nil
}

func sameHostTransportSource(a, b hostStateSource) bool {
	return a.exists == b.exists && a.generation == b.generation && a.path == b.path && a.root.Known() && b.root.Known() &&
		a.root.Version == b.root.Version && a.root.Identity == b.root.Identity && a.root.Mode == b.root.Mode && a.root.Directory && b.root.Directory
}

func (s *hostTransportStamp) checkObservation(ctx context.Context, o *HostStateObservation, c *Conn) error {
	if err := s.checkConn(c); err != nil {
		return err
	}
	if err := o.checkOwner(ctx); err != nil {
		return err
	}
	if !SameEntry(s.host, o.prospective) || !sameHostTransportSource(s.source, o.source) {
		return hostStateRemovalError("the connection belongs to an older host membership Source")
	}
	if s.hostID == "" && (s.priorID != "" || o.expectedID != "") {
		return grantIdentityError(s.host)
	}
	var held *heldLock
	switch s.kind {
	case hostTransportSaved:
		if o.role != hostStateSaved || !s.source.exists {
			return hostStateRemovalError("a saved connection cannot acquire another observation role")
		}
		held = o.op
	case hostTransportRegistration:
		if o != s.registration || o.role != hostStateRegistration {
			return hostStateRemovalError("a registration connection cannot promote after publication")
		}
		held = o.capturedOP.Load()
	default:
		return hostStateRemovalError("an unobserved unsaved connection has no state publication authority")
	}
	if held == nil || held.terminal.Load() {
		return hostStateRemovalError("the connection has no current matching actual OP")
	}
	if err := held.checkStateOperation(ctx); err != nil {
		return err
	}
	if err := s.parent.CheckRoot(held.native.root); err != nil {
		return err
	}
	return s.op.CheckCreatedFile(held.native.root, held.native.name, held.native.file, 0600)
}

// Read only the canonical tier file. A pending registration's prospective
// host can differ from the still-saved Was; only that immutable Was supplies
// prior identity. No migration, permission question, pin acquisition or write.
func transportPriorHostID(ctx context.Context, o *HostStateObservation, h Host) (string, error) {
	if o == nil || !SameEntry(o.prospective, h) || (o.role != hostStateSaved && o.role != hostStateRegistration) {
		return "", hostStateRemovalError("prior identity has no original saved or registration observation")
	}
	if err := withHostMembershipContext(ctx, o.checkMembership); err != nil {
		return "", err
	}
	prior := &o.prospective
	if o.role == hostStateRegistration {
		prior = o.original
	}
	var hostID string
	if prior != nil {
		tiers, err := readTiersFile()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if err == nil {
			entry, _ := tiers.Entry(prior.Name)
			hostID = entry.HostID
		}
	}
	if err := withHostMembershipContext(ctx, o.checkMembership); err != nil {
		return "", err
	}
	return hostID, nil
}

// Main calls this before/after each actual wake/probe/install/start action.
// Provisional validity is NOT saved or registration auto-action permission.
func checkHostTransportAttempt(ctx context.Context, attempt *hostTransportAttempt, h Host) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if attempt == nil || !attempt.active.Load() || attempt.finished.Load() || ctx.Value(hostTransportAttemptKey{}) != attempt ||
		!SameEntry(attempt.host, h) || ctx.Value(hostMembershipContextKey{}) != nil || ctx.Value(hostMutationContextKey{}) != nil ||
		ctx.Value(hostForgetMembershipKey{}) != nil || ctx.Value(hostStateInspectionKey{}) != nil || ctx.Value(hostTerminalInspectionKey{}) != nil {
		return hostStateRemovalError("the transport action has no same active unfinished original attempt")
	}
	if attempt.kind == hostTransportProvisional {
		saved, err := SavedEntry(h.Name)
		if err != nil {
			return err
		}
		if saved != nil {
			return hostStateRemovalError("the unsaved transport name became saved; start a new connection")
		}
		return nil
	}
	o := attempt.observation
	held, _ := ctx.Value(heldKey{h.Name}).(*heldLock)
	if o == nil || held == nil || held != attempt.op || held.terminal.Load() {
		return hostStateRemovalError("the transport action changed its original physical OP")
	}
	return withHostMembershipContext(ctx, o.checkMembership)
}
