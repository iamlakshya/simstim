package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

// SessionMachineResourceV1 is an actual provider creation owner. Implementations
// retain the original client, credentials, locator and returned resource ID.
// Host is reporting data; it cannot reconstruct Rollback authority.
type SessionMachineResourceV1 interface {
	// Host reports the actual creation without transferring its authority.
	Host() Host
	// Lifecycle reaches only that creation through its original provider client.
	Lifecycle() Lifecycle
	// Rollback destroys the actual creation after a failed session start.
	Rollback(context.Context) error
	// Close revokes the handle after every owned process has joined.
	Close() error
}

type sessionMachineOwnerKeyV1 struct{}

// SessionMachineOwnerV1 keeps the original creation and same-writer saved
// Source through registration, preparation and rollback. It is never serialised.
type SessionMachineOwnerV1 struct {
	mu                sync.Mutex
	request           HostCreationRequest
	resource          SessionMachineResourceV1
	capture           *hostPrepareCommitCapture
	registration      context.Context
	release           func()
	downgrade         bool
	committed, closed atomic.Bool
	startedID         string
	connections       map[*Conn]struct{}
}

// SessionMachineNameV1 chooses the session name before making its machine.
func SessionMachineNameV1(name string) (string, error) {
	if name == "" {
		var random [6]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", fmt.Errorf("name the new provider session: %w", err)
		}
		name = "session-" + hex.EncodeToString(random[:])
	}
	if err := CheckSessionName(name); err != nil {
		return "", err
	}
	if !ValidHostName("m-" + name) {
		return "", &UserError{Msg: "the session name cannot also name its provider machine", Hint: "Pass --name with a shorter name of letters, digits, dots, dashes or underscores.", Code: "bad_name", Kind: KindUsage}
	}
	return name, nil
}

// BeginSessionMachineV1 calls one closed producer in one original absent-name
// reservation, then publishes its exact machine with shared tier. The caller
// must FinishRegistration and defer Finish even when this returns an error.
func BeginSessionMachineV1(ctx context.Context, request HostCreationRequest, session string, downgrade bool, create func(context.Context) (Host, SessionMachineResourceV1, error)) (owner *SessionMachineOwnerV1, returned error) {
	if request.Provider != "e2b" || request.Name != "m-"+session || create == nil {
		return nil, sessionMachineErrorV1("the machine creation does not match its original session")
	}
	owner = &SessionMachineOwnerV1{request: request, downgrade: downgrade, capture: &hostPrepareCommitCapture{name: request.Name}}
	owned, release, err := BeginHostCreationContext(owner.Context(ctx), request)
	if err != nil {
		return owner, err
	}
	owner.release = release
	if err := CheckHostCreationRemovalContext(owned, request.Name); err != nil {
		return owner, err
	}
	if err := StartHostCreationContext(owned); err != nil {
		return owner, err
	}
	defer EndHostCreationAttemptContext(owned)
	host, resource, err := create(owned)
	owner.resource = resource
	if err != nil {
		return owner, err
	}
	if resource == nil || resource.Lifecycle() == nil || !SameEntry(host, resource.Host()) || host.Name != request.Name || host.Machine == nil || host.Machine.Provider != "e2b" || strings.TrimSpace(host.Machine.ID) == "" {
		return owner, sessionMachineErrorV1("the provider did not return its original owned machine")
	}
	host = cloneObservationHost(host)
	host.Machine.Session = session
	host, err = BindHostCreationResultContext(owned, host)
	if err != nil {
		return owner, err
	}
	if err := UpdateHosts(owned, HostChange{Kind: "host create", Now: &host, Pin: true, Tier: TierShared}); err != nil {
		return owner, err
	}
	// Continue requires the completed producer attempt, not a reconstructed row.
	EndHostCreationAttemptContext(owned)
	owner.registration, err = ContinueHostCreationContext(owned)
	return owner, err
}

// CheckSessionMachineCreationContextV1 is called by the production producer
// before credentials/create and after its actual reply. It never refreshes an
// old reservation after cancellation or a membership change.
func CheckSessionMachineCreationContextV1(ctx context.Context, request HostCreationRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	owner, _ := ctx.Value(sessionMachineOwnerKeyV1{}).(*SessionMachineOwnerV1)
	o, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation)
	if owner == nil || owner.request != request || owner.closed.Load() || o == nil || o.role != hostStateCreation || o.creation == nil || o.creation.request != request || !o.creation.started || o.creation.finished.Load() {
		return sessionMachineErrorV1("the provider has no live original session-machine creation")
	}
	if err := o.checkOwner(ctx); err != nil {
		return err
	}
	return withHostMembershipContext(ctx, o.checkMembership)
}

// Context carries only this private original owner and the canonical writer's
// commit capture. It supplies no checked host identity by itself.
func (o *SessionMachineOwnerV1) Context(ctx context.Context) context.Context {
	return context.WithValue(ctx, sessionMachineOwnerKeyV1{}, o)
}

func (o *SessionMachineOwnerV1) recordCommittedV1(ctx context.Context, change HostChange, out []Host) error {
	if o == nil || o.closed.Load() || change.Now == nil || change.Now.Name != o.request.Name {
		return nil
	}
	return o.capture.recordCommitted(ctx, change, out)
}

func (o *SessionMachineOwnerV1) checkNextHostV1(host Host) error {
	if o == nil || o.closed.Load() || o.resource == nil || host.Machine == nil {
		return sessionMachineErrorV1("the machine writer lost its original creator")
	}
	original := o.resource.Host()
	if original.Machine == nil || original.Name != host.Name || original.Machine.ID != host.Machine.ID || original.Machine.Provider != host.Machine.Provider || original.Machine.Endpoint != host.Machine.Endpoint || original.Machine.TransportURL != host.Machine.TransportURL || host.Machine.Session != strings.TrimPrefix(o.request.Name, "m-") {
		return sessionMachineErrorV1("the machine writer changed its original created resource")
	}
	if host.Machine.SessionID != "" && host.Machine.SessionID != o.startedID {
		return sessionMachineErrorV1("a machine row cannot install another session's ownership")
	}
	return nil
}

// RegistrationContext retains the original creator's physical operation.
func (o *SessionMachineOwnerV1) RegistrationContext() context.Context { return o.registration }

// Host returns the latest actual writer's saved entry, never provider metadata.
func (o *SessionMachineOwnerV1) Host() Host {
	if o == nil || o.capture == nil || o.capture.target == nil {
		return Host{}
	}
	return o.capture.target.Host()
}

// FinishRegistration releases the creation OP before saved-target preparation.
func (o *SessionMachineOwnerV1) FinishRegistration() error {
	if o.release != nil {
		o.release()
		o.release = nil
	}
	o.registration = nil
	if o.capture.target == nil {
		return sessionMachineErrorV1("registration produced no original saved target")
	}
	return nil
}

// Target returns the same canonical writer's current preparation target.
func (o *SessionMachineOwnerV1) Target() *HostPrepareTargetV1 { return o.capture.target }

// Commit keeps the machine after the start operation has completed. Failed
// attachment or output after this point cannot destroy a running session.
func (o *SessionMachineOwnerV1) Commit(ctx context.Context) error {
	if o == nil || o.closed.Load() || o.committed.Load() || o.startedID == "" || o.capture.target == nil {
		return sessionMachineErrorV1("the machine has no acknowledged original session start")
	}
	target := o.capture.target
	ctx, release, err := LockHost(o.Context(ctx), target.Name(), "host set")
	if err != nil {
		return err
	}
	defer release()
	if err := target.Check(ctx); err != nil {
		return err
	}
	was, now := target.Host(), target.Host()
	now.Machine.SessionID = o.startedID
	check := func(locked context.Context) error { return target.checkLocked(locked, target.hostID) }
	if err := UpdateHosts(context.WithValue(ctx, hostPrepareRecordCheckKey{}, check), HostChange{Kind: "host set", Was: &was, Now: &now}); err != nil {
		return err
	}
	o.mu.Lock()
	o.committed.Store(true)
	o.mu.Unlock()
	return nil
}

// Only NewSession's actual successful call path records this prospective ID.
// A decoded row or a caller-supplied session cannot install machine ownership.
func acceptSessionMachineStartV1(ctx context.Context, c *Conn, args NewArgs, session Session) error {
	if !IsSessionMachineV1(c.Host) {
		return nil
	}
	o, _ := ctx.Value(sessionMachineOwnerKeyV1{}).(*SessionMachineOwnerV1)
	if err := checkSessionMachineNewV1(ctx, c); err != nil {
		return err
	}
	r := grantRequestOf(ctx)
	if r == nil || args.ID == "" || session.ID != args.ID || args.Name != c.Host.Machine.Session {
		return sessionMachineErrorV1("the machine start did not acknowledge its original reserved session")
	}
	r.mu.Lock()
	same := r.id == session.ID
	r.mu.Unlock()
	if !same {
		return grantOperationError()
	}
	if err := o.capture.target.checkConn(ctx, c); err != nil {
		return err
	}
	o.startedID = session.ID
	return nil
}

func checkSessionMachineNewV1(ctx context.Context, c *Conn) error {
	if !IsSessionMachineV1(c.Host) {
		return nil
	}
	o, _ := ctx.Value(sessionMachineOwnerKeyV1{}).(*SessionMachineOwnerV1)
	if o == nil || o.closed.Load() || o.committed.Load() || o.startedID != "" || c.route.machineOwner != o || c.Host.Machine.SessionID != "" {
		return sessionMachineErrorV1("this machine belongs to its original session")
	}
	return nil
}

// Finish destroys only the original created resource after failed new. A
// failed destroy keeps its saved host so host rm --destroy can retry.
func (o *SessionMachineOwnerV1) Finish(ctx context.Context) (returned error) {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	if o.closed.Load() {
		o.mu.Unlock()
		return nil
	}
	o.closed.Store(true)
	keep := o.committed.Load()
	o.mu.Unlock()
	if o.release != nil {
		o.release()
		o.release = nil
	}
	if o.resource == nil {
		return nil
	}
	deadline := time.Now().Add(time.Minute)
	if original, ok := ctx.Deadline(); ok && original.Before(deadline) {
		deadline = original
	}
	cleanup, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	defer cancel()
	var rollbackErr error
	if !keep {
		rollbackErr = o.resource.Rollback(cleanup)
	}
	if err := o.joinConnectionsV1(cleanup); err != nil {
		return errors.Join(rollbackErr, err)
	}
	defer func() { returned = errors.Join(returned, o.resource.Close()) }()
	if keep {
		return nil
	}
	if rollbackErr != nil {
		return &UserError{Msg: fmt.Sprintf("the failed session's machine could not be removed: %s", CleanLine(rollbackErr.Error())), Hint: brand.Name + " host rm " + ShellQuote(o.request.Name) + " --destroy --confirm " + ShellQuote(o.request.Name), Code: "machine_cleanup", Kind: KindFailed}
	}
	if target := o.capture.target; target != nil {
		return forgetSessionMachineTargetV1(cleanup, target)
	}
	return nil
}

func forgetSessionMachineTargetV1(ctx context.Context, target *HostPrepareTargetV1) (failure error) {
	removal, err := OpenSavedHostRemovalV1(ctx, target.Name())
	if err != nil {
		return err
	}
	defer func() { failure = errors.Join(failure, removal.Close()) }()
	if err := target.Check(removal.Context()); err != nil {
		return err
	}
	if !SameEntry(removal.host, target.host) || !sameHostTransportSource(removal.source, target.source) {
		return sessionMachineErrorV1("the failed creation's original saved Source was replaced before forget")
	}
	return removal.Forget(removal.Context())
}

func sessionMachineErrorV1(message string) error {
	return &UserError{Msg: message, Hint: "Start a fresh new --machine e2b request, or inspect the saved machine with host ls.", Code: "machine_source", Kind: KindRefused}
}

// IsSessionMachineV1 is display/selection metadata, never root or destroy authority.
func IsSessionMachineV1(h Host) bool { return h.Machine != nil && h.Machine.Session != "" }

func sessionMachineLifecycleV1(ctx context.Context, host Host) (Lifecycle, *SessionMachineOwnerV1, error) {
	o, _ := ctx.Value(sessionMachineOwnerKeyV1{}).(*SessionMachineOwnerV1)
	if o == nil {
		return nil, nil, nil
	}
	if o.resource == nil || o.closed.Load() || host.Name != o.request.Name || host.Machine == nil {
		return nil, nil, sessionMachineErrorV1("the original machine owner is no longer active")
	}
	original := o.resource.Host()
	if original.Machine == nil || host.Machine.Provider != original.Machine.Provider || host.Machine.ID != original.Machine.ID {
		return nil, nil, sessionMachineErrorV1("the saved machine differs from its actual provider creation")
	}
	return o.resource.Lifecycle(), o, nil
}
