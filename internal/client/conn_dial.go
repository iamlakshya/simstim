package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/agentbridge"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client/helpers"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// Dial starts the bridge on the host and does the hello. For a saved host (one
// the user added, and so agreed to the helper on), a missing helper of this
// version is installed first: an upgrade never needs the user.
//
// A host that a provider made is asked for its state first, and started or
// resumed when it is stopped, unless ctx comes from WithoutWake. In a ctx
// from WithoutInstall, a missing helper is an error, and nothing is
// installed or pruned.
func Dial(ctx context.Context, h Host, interactive bool) (*Conn, error) {
	return dialWithPreparation(ctx, h, interactive, nil)
}

// A trusted internal preparation receives only this original operation's
// actual route after pin acceptance. It cannot select another route or cause
// a Source refresh. Ordinary Dial supplies no preparation.
func dialWithPreparation(ctx context.Context, h Host, interactive bool, beforeStart func(context.Context, Transport) error) (*Conn, error) {
	// A selected launcher action may only narrow this dial to its actual
	// accepted parent. Check before tier resolution, route lookup or auth.
	if c, restricted, err := restrictedHostDialV1(ctx, h, beforeStart); restricted {
		return c, err
	}
	if observation, _ := ctx.Value(hostStateObservationKey{}).(*HostStateObservation); observation != nil && observation.role == hostStateRegistration {
		// The real add owns prospective Now while original Was is still saved.
		// Its explicit add choice is distinct from resolving Was as though Now
		// were already saved. No temporary OP or new observation is fabricated.
		if _, err := captureHostStateObservation(ctx, h); err != nil {
			return nil, err
		}
	} else if _, err := ResolveHostTier(ctx, h); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !interactive {
		ctx = quietConnectionNotes(ctx)
	}
	callerContext := ctx
	owned, endObservation, transportAttempt, err := beginHostTransportObservation(ctx, h)
	if err != nil {
		return nil, err
	}
	defer endObservation()
	ctx = owned
	if !installAllowed(ctx) {
		// A client built with go install learns the version of its helper
		// when it loads the helpers, which only Probe does otherwise. Load
		// them here, from this computer's cache only, so that the dial
		// starts the right helper, and never waits on a download. With
		// nothing cached, the dial goes on with the version it has, and a
		// missing helper is helper_missing, whose Try: line is host
		// upgrade, which downloads them.
		_ = helpers.Cached(version.Version, CacheDir())
	}
	if err := checkHostTransportAttempt(ctx, transportAttempt, h); err != nil {
		return nil, err
	}
	r, err := routeTo(ctx, h, interactive)
	if err != nil {
		return nil, err
	}
	if err := checkHostTransportAttempt(ctx, transportAttempt, h); err != nil {
		return nil, err
	}
	if err := pinRoute(ctx, h, r.t, interactive); err != nil {
		// A stopped cloud host cannot show its key either: start it first.
		if !mayWake(ctx, h, err) {
			return nil, err
		}
		if err := checkHostTransportWake(ctx, transportAttempt, h); err != nil {
			return nil, err
		}
		if werr := wake(ctx, h); werr != nil {
			return nil, werr
		}
		if err := pinRoute(ctx, h, r.t, interactive); err != nil {
			return nil, err
		}
	}
	if beforeStart != nil {
		if err := checkHostTransportAttempt(ctx, transportAttempt, h); err != nil {
			return nil, err
		}
		if err := beforeStart(ctx, r.t); err != nil {
			return nil, err
		}
		if err := checkHostTransportAttempt(ctx, transportAttempt, h); err != nil {
			return nil, err
		}
	}
	c, err := dialObserved(ctx, callerContext, h, r, transportAttempt)
	if mayWake(ctx, h, err) {
		// A stopped cloud host: start it through its provider script, then try again.
		if err := checkHostTransportWake(ctx, transportAttempt, h); err != nil {
			return nil, err
		}
		if werr := wake(ctx, h); werr != nil {
			return nil, werr
		}
		c, err = dialObserved(ctx, callerContext, h, r, transportAttempt)
	}
	if !errors.Is(err, ErrNotInstalled) {
		return c, err
	}
	if transportAttempt.kind != hostTransportSaved {
		return nil, hostStateRemovalError("an unsaved transport attempt cannot acquire automatic installation authority")
	}
	if _, ferr := FindHost(h.Name); ferr != nil {
		return nil, &UserError{Msg: fmt.Sprintf("the %s helper is not installed on %s", brand.Name, h.Name), Hint: brand.Name + " host add " + h.Name, Code: "helper_missing"}
	}
	if !installAllowed(ctx) {
		return nil, &UserError{Msg: fmt.Sprintf("the helper %s is not on %s", HelperVersion(), h.Name), Hint: brand.Name + " host upgrade " + h.Name, Code: "helper_missing"}
	}
	if err := checkHostTransportAttempt(ctx, transportAttempt, h); err != nil {
		return nil, err
	}
	p, perr := Probe(ctx, r.t)
	if perr != nil {
		return nil, perr
	}
	if !p.Installed {
		if err := checkHostTransportAttempt(ctx, transportAttempt, h); err != nil {
			return nil, err
		}
		if ierr := Install(ctx, r.t, p); ierr != nil {
			return nil, ierr
		}
	}
	return dialObserved(ctx, callerContext, h, r, transportAttempt)
}

// mayWake reports whether err, from a connection to h, is one that waking
// h through its provider script may cure: h could not be reached, and its
// script wakes it on attach.
func mayWake(ctx context.Context, h Host, err error) bool {
	var ue *UserError
	return err != nil && h.Provider != nil && h.Provider.WakeOnAttach && wakeAllowed(ctx) &&
		errors.As(err, &ue) && strings.HasPrefix(ue.Msg, "cannot reach")
}

func dial(ctx context.Context, h Host, rt route) (*Conn, error) {
	callerContext := ctx
	owned, endObservation, attempt, err := beginHostTransportObservation(ctx, h)
	if err != nil {
		return nil, err
	}
	defer endObservation()
	return dialObserved(owned, callerContext, h, rt, attempt)
}

func dialObserved(ctx, callerContext context.Context, h Host, rt route, transportAttempt *hostTransportAttempt) (result *Conn, resultErr error) {
	var nativeStart *ownedDialStartV1
	defer func() {
		if resultErr != nil && nativeStart != nil {
			resultErr = &ownedDialFailureV1{start: nativeStart, actual: resultErr}
		}
	}()
	if rt.agentOnly {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, agentbridge.HandshakeTimeout)
		defer cancel()
	}
	if err := checkHostTransportAttempt(ctx, transportAttempt, h); err != nil {
		return nil, err
	}
	remote := RemoteHelper(HelperVersion()) + " bridge"
	if rt.agentOnly {
		remote = RemoteHelper(HelperVersion()) + " bridge-agent"
	}
	ka, _ := rt.t.(KeepAliver)
	if ka != nil && !rt.agentOnly && !eventsDial(ctx) && !inputContinuationDial(ctx) {
		// This transport does not end the bridge when the client goes, so the
		// bridge ends itself when the pings stop.
		remote += fmt.Sprintf(" --idle-exit %d", int(BridgeIdleExit.Seconds()))
	}
	// Startup follows the caller until the host sends a valid hello. The
	// connection then owns the process context, including its cancellation.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	procCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stopStartup := context.AfterFunc(ctx, cancel)
	defer stopStartup()
	started := false
	defer func() {
		if !started {
			cancel()
		}
	}()
	nativeStart = beginOwnedNewDialStartV1(ctx)
	proc, err := dialStartOwnedV1(procCtx, rt.t, remote, nativeStart)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	stdin := proc.Stdin()
	var stream chan proto.Frame
	var trace *connTrace
	if !rt.agentOnly {
		stream = make(chan proto.Frame, 4096)
		trace = newConnTrace(ctx)
	}
	c := &Conn{Host: h, proc: proc, cancel: cancel, hint: rt.t.ReachHint(), stdin: stdin, w: proto.NewWriter(stdin),
		pending: map[uint64]chan proto.Response{}, stream: stream, done: make(chan struct{}), tr: trace}
	c.route = rt
	c.procDone = make(chan struct{})
	recordOwnedNewDialConnectionV1(ctx, c, nativeStart)
	c.agentOnly = rt.agentOnly
	readerSize := 256 << 10
	if rt.agentOnly {
		readerSize = agentbridge.MaxIdentityHello
	}
	r := bufio.NewReaderSize(proc.Stdout(), readerSize)
	role := "client"
	if c.agentOnly {
		role = agentbridge.Role
	}
	if err := c.w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: role}); err != nil {
		return nil, c.startError(ctx, err)
	}
	type res struct {
		f   proto.Frame
		err error
	}
	first := make(chan res, 1)
	go func() {
		if rt.agentOnly {
			f, err := proto.ReadLimit(r, agentbridge.MaxIdentityHello)
			first <- res{f, err}
			return
		}
		f, err := proto.Read(r)
		first <- res{f, err}
	}()
	select {
	case <-ctx.Done():
		c.killStartup()
		return nil, ctx.Err()
	case x := <-first:
		if x.err != nil {
			if !errors.Is(x.err, io.EOF) {
				c.kill()
			}
			return nil, c.startError(ctx, x.err)
		}
		if x.f.Type != proto.THello {
			c.killStartup()
			return nil, errors.New("unexpected first frame from host")
		}
		var hello proto.Hello
		var decodeErr error
		if c.agentOnly {
			hello, decodeErr = agentbridge.DecodeHello(x.f.Payload)
		} else {
			decodeErr = json.Unmarshal(x.f.Payload, &hello)
		}
		if decodeErr != nil {
			c.killStartup()
			return nil, fmt.Errorf("read the host's hello: %w", decodeErr)
		}
		if err := hello.Check(version.Protocol); err != nil {
			c.killStartup()
			return nil, &UserError{Msg: CleanLine(err.Error()), Hint: brand.Name + " host add " + h.Name + " (reinstalls the helper)", Code: "protocol"}
		}
		if c.agentOnly && hello.Role != "bridge" {
			c.killStartup()
			return nil, agentChannelError("the agent helper sent the wrong identity role")
		}
		if hello.HostID != "" && !ValidHostID(hello.HostID) {
			c.killStartup()
			return nil, &UserError{Msg: fmt.Sprintf("the helper on %s sent an invalid host ID", h.Name), Hint: brand.Name + " host doctor " + h.Name, Code: "protocol"}
		}
		changed, err := acceptTransportHelloIdentity(ctx, transportAttempt, h, hello.HostID)
		if err != nil {
			c.killStartup()
			return nil, err
		}
		c.hostID = hello.HostID
		c.hostIDChanged = changed
		c.Features = hello.Features
	}
	stopStartup()
	if err := ctx.Err(); err != nil {
		c.killStartup()
		return nil, err
	}
	if err := finishHostTransportObservation(ctx, transportAttempt, c); err != nil {
		c.killStartup()
		return nil, err
	}
	started = true
	c.lastRecv.Store(time.Now().UnixNano())
	c.watchProcess()
	if c.agentOnly {
		c.agentReader = r
		return c, nil
	}
	if exclusiveContinuationDial(callerContext) {
		c.execReader = r
		c.execReadDone = make(chan struct{})
		if ka != nil && execDial(callerContext) {
			c.execAlive = ka.KeepAlive()
		}
	} else {
		go c.readLoop(r)
		c.startBridgeDebug(callerContext)
		if ka != nil {
			go c.keepAlive(ka.KeepAlive())
		}
	}
	if l, ok := rt.lc.(Leaser); ok {
		c.leaser = l
		if !exclusiveContinuationDial(callerContext) {
			c.machineLeaseDone = make(chan struct{})
			go func() {
				defer close(c.machineLeaseDone)
				keepLease(context.WithoutCancel(callerContext), c, l, leasePeriod)
			}()
		}
	}
	if !c.agentOnly && !exclusiveContinuationDial(callerContext) && c.hostTransport != nil && c.hostTransport.kind == hostTransportSaved {
		if err := c.CheckSavedSource(callerContext); err != nil {
			c.Close()
			return nil, err
		}
		if err := ReplayPendingPolicy(callerContext, c); err != nil {
			warn(callerContext, "policy_pending", "Policy is still pending on "+CleanLine(c.Host.Name), "Run policy apply to retry with the current local policy.")
		}
		if err := callerContext.Err(); err != nil {
			c.Close()
			return nil, err
		}
		if err := c.CheckSavedSource(callerContext); err != nil {
			c.Close()
			return nil, err
		}
	}
	if rt.machineOwner != nil {
		rt.machineOwner.retainConnectionV1(c)
	}
	return c, nil
}

// killStartup stops an invalid or cancelled handshake at once. Wait still
// reaps the transport and releases its private diagnostic files.

// A provisional SDK read cannot gain provider mutation authority when a
// same-named host appears later. Registration keeps its own typed proof.
func checkHostTransportWake(ctx context.Context, attempt *hostTransportAttempt, h Host) error {
	if err := checkHostTransportAttempt(ctx, attempt, h); err != nil {
		return err
	}
	if attempt.kind == hostTransportProvisional {
		return hostStateRemovalError("an unsaved read-only transport cannot wake a host")
	}
	return nil
}

func acceptTransportHelloIdentity(ctx context.Context, attempt *hostTransportAttempt, h Host, hostID string) (bool, error) {
	if err := checkHostTransportAttempt(ctx, attempt, h); err != nil {
		return false, err
	}
	if attempt.continuation != nil {
		if hostID != attempt.continuation.parent.HostID() || !ValidHostID(hostID) {
			return false, grantIdentityError(h)
		}
		return false, nil
	}
	switch attempt.kind {
	case hostTransportSaved:
		return noteHostID(ctx, h, hostID)
	case hostTransportRegistration:
		// Final saveAdded owns the new tier/identity CAS. A prospective Hello
		// must not overwrite still-saved Was before that commit.
		return attempt.priorID != "" && attempt.priorID != hostID, nil
	case hostTransportProvisional:
		// Continued absence is checked again at finish. This route never
		// acquires a saved tier entry, even if a name appears during Hello.
		return false, nil
	default:
		return false, hostStateRemovalError("the accepted Hello has no original transport role")
	}
}
