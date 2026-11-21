package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

const maxForwardedPorts = 32

type portForward struct {
	openMu sync.Mutex // actual per-grant physical open lifetime; revokers join after portMu unlock
	port   int
	until  time.Time
	ctx    context.Context
	cancel context.CancelFunc
	listen net.Listener
	wg     sync.WaitGroup
	done   chan struct{}
	limit  chan struct{}
}

func portFailure(message string) *proto.Error {
	return &proto.Error{Code: "port_refused", Message: message, Hint: "Check the exact port grant and the sandbox, then forward it again."}
}

func (s *Supervisor) portRequest(c *client, request proto.Request) bool {
	switch request.Op {
	case "ports.view":
		view := s.owner().portForwardView()
		c.reply(request.ID, view, nil)
		return true
	case "ports":
		ports, err := s.listSandboxPorts()
		c.reply(request.ID, ports, toProtoErrOrNil(err))
		return true
	case "port.grant":
		var args api.PortArgsV1
		if !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || args.Version != 1 || args.ID != s.id || args.Port < 1 || args.Port > 65535 {
			c.reply(request.ID, nil, portFailure("invalid port grant request"))
			return true
		}
		root := s.owner()
		if err := root.grantPort(args.Port); err != nil {
			c.reply(request.ID, nil, toProtoErr(err))
			return true
		}
		c.reply(request.ID, api.PortGrantResultV1{Version: 1, Port: args.Port, Socket: paths.PortSocket(root.id, args.Port)}, nil)
		return true
	}
	return false
}

func (s *Supervisor) grantPort(port int) error {
	s.polMu.Lock()
	defer s.polMu.Unlock()
	s.mu.Lock()
	running := s.rec.phase == host.LifeRunning
	s.mu.Unlock()
	if !running {
		return portFailure("the sandbox is not running")
	}
	err := host.WithSessionLease(s.id, 5*time.Second, func() error {
		s.saveMu.Lock()
		defer s.saveMu.Unlock()
		var snapshot host.Manifest
		return s.writeManifestChange(&snapshot, false, nil, nil, func(target *host.Manifest) error {
			policy, layers, _, err := host.ApplyPolicyAdd(target.Layers, api.PolicyAddArgs{Door: api.DoorPort, Allow: true, Target: strconv.Itoa(port)})
			if err != nil {
				return err
			}
			target.Policy, target.Layers = &policy, layers
			return nil
		}, func(target *host.Manifest) {
			policy := api.ClonePolicy(*target.Policy)
			s.m.Policy, s.m.Layers = &policy, api.CloneLayers(target.Layers)
		})
	})
	if err != nil {
		return err
	}
	if err := s.reconcilePorts(); err != nil {
		return err
	}
	s.portMu.Lock()
	listener := s.portListeners[port]
	active := listener != nil && listener.ctx.Err() == nil
	s.portMu.Unlock()
	if !active {
		return portFailure("a narrower policy refuses the requested port")
	}
	s.audit.write("port_grant", map[string]any{"port": port})
	return nil
}

func (s *Supervisor) reconcilePorts() error {
	s = s.owner()
	s.mu.Lock()
	var policy *api.Policy
	if s.m.Policy != nil {
		copy := api.ClonePolicy(*s.m.Policy)
		policy = &copy
	}
	running := s.rec.phase == host.LifeRunning
	s.mu.Unlock()
	wanted := map[int]time.Time{}
	if policy != nil && running {
		for _, rule := range policy.Rules {
			if rule.Door != api.DoorPort {
				continue
			}
			port, err := api.ParsePortTarget(rule.Target)
			if err != nil {
				s.closePortListeners(false)
				return portFailure("a saved port rule is invalid")
			}
			allowed, until, err := host.PortPolicyGrant(policy, port, time.Now())
			if err != nil {
				s.closePortListeners(false)
				return err
			}
			if allowed {
				wanted[port] = until
			}
		}
	}
	if len(wanted) > maxForwardedPorts {
		s.closePortListeners(false)
		return portFailure("the sandbox has more than 32 forwarded port grants")
	}
	s.portMu.Lock()
	if s.portsClosed {
		s.portMu.Unlock()
		return portFailure("the sandbox's forwarding owner is closed")
	}
	if s.portListeners == nil {
		s.portListeners = make(map[int]*portForward)
	}
	var ended []*portForward
	for port, current := range s.portListeners {
		until, exists := wanted[port]
		if !exists || current.ctx.Err() != nil || !current.until.Equal(until) {
			delete(s.portListeners, port)
			current.cancel()
			_ = current.listen.Close()
			ended = append(ended, current)
		}
	}
	var failure error
	for port, until := range wanted {
		if s.portListeners[port] != nil {
			continue
		}
		current, err := s.newPortListener(port, until)
		if err != nil {
			failure = errors.Join(failure, err)
			continue
		}
		s.portListeners[port] = current
		go s.acceptPortForward(current)
	}
	s.portMu.Unlock()
	for _, current := range ended {
		current.openMu.Lock()
		current.openMu.Unlock()
		<-current.done
	}
	return failure
}

func (s *Supervisor) newPortListener(port int, until time.Time) (*portForward, error) {
	name := paths.PortSocket(s.id, port)
	if err := paths.EnsurePrivateDir(filepath.Dir(filepath.Dir(name))); err != nil {
		return nil, portFailure("the private port socket directory is unavailable")
	}
	if err := paths.EnsurePrivateDir(filepath.Dir(name)); err != nil {
		return nil, portFailure("the private session port directory is unavailable")
	}
	if info, err := os.Lstat(name); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, portFailure("the port socket path already holds another file")
		}
		if err := os.Remove(name); err != nil {
			return nil, portFailure("an old port socket cannot be removed")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, portFailure("the port socket cannot be checked")
	}
	ctx, cancel := context.WithCancel(s.lifetimeContext())
	if !until.IsZero() {
		cancel()
		ctx, cancel = context.WithDeadline(s.lifetimeContext(), until)
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "unix", name)
	if err != nil {
		cancel()
		return nil, portFailure("the private port socket cannot listen")
	}
	if err := os.Chmod(name, 0o600); err != nil {
		cancel()
		_ = listener.Close()
		return nil, portFailure("the port socket cannot be made private")
	}
	return &portForward{port: port, until: until, ctx: ctx, cancel: cancel, listen: listener, done: make(chan struct{}), limit: make(chan struct{}, 32)}, nil
}

func (s *Supervisor) acceptPortForward(owner *portForward) {
	defer func() {
		close(owner.done)
		if errors.Is(owner.ctx.Err(), context.DeadlineExceeded) {
			_ = s.reconcilePorts() // an expired narrow rule may reveal a wider saved grant
		}
	}()
	defer owner.wg.Wait()
	defer owner.cancel()
	defer owner.listen.Close()
	stop := context.AfterFunc(owner.ctx, func() { _ = owner.listen.Close() })
	defer stop()
	for {
		connection, err := owner.listen.Accept()
		if err != nil {
			return
		}
		s.portMu.Lock()
		s.mu.Lock()
		allowed, _, grantErr := host.PortPolicyGrant(s.m.Policy, owner.port, time.Now())
		s.mu.Unlock()
		valid := !s.portsClosed && s.portListeners[owner.port] == owner && owner.ctx.Err() == nil && allowed && grantErr == nil
		if valid {
			select {
			case owner.limit <- struct{}{}:
				owner.wg.Add(1)
			default:
				valid = false
			}
		}
		s.portMu.Unlock()
		if !valid {
			_ = connection.Close()
			continue
		}
		if !host.FullPeer(connection, s.id, s.peerCheck) {
			owner.wg.Done()
			<-owner.limit
			_ = connection.Close()
			continue
		}
		go func() {
			defer owner.wg.Done()
			defer func() { <-owner.limit }()
			defer connection.Close()
			stream, err := s.link.OpenGuarded(owner.ctx, link.KindPort, proto.PortHeaderV1{Version: 1, Port: owner.port}, func(ctx context.Context) (func(), error) { return s.portGrantTurnV1(ctx, owner) })
			if err != nil {
				return
			}
			defer stream.Close()
			s.audit.write("port", map[string]any{"port": owner.port})
			_ = link.RelayDuplex(owner.ctx, connection, stream)
		}()
	}
}

func (s *Supervisor) closePortListeners(terminal bool) {
	s.portMu.Lock()
	s.portsClosed = s.portsClosed || terminal
	var listeners []*portForward
	for port, listener := range s.portListeners {
		delete(s.portListeners, port)
		listener.cancel()
		_ = listener.listen.Close()
		listeners = append(listeners, listener)
	}
	s.portMu.Unlock()
	for _, listener := range listeners {
		listener.openMu.Lock()
		listener.openMu.Unlock()
		<-listener.done
	}
}

func (s *Supervisor) listSandboxPorts() ([]api.Port, error) {
	root := s.owner()
	ctx, cancel := context.WithTimeout(root.lifetimeContext(), 5*time.Second)
	defer cancel()
	var claims []api.Port
	if err := root.ctl.requestDetached(ctx, proto.CtlPortsV1, struct{}{}, &claims); err != nil {
		return nil, portFailure("the guest's claimed listening ports are unavailable")
	}
	if len(claims) > 128 {
		return nil, portFailure("the guest sent too many listening port claims")
	}
	byPort := map[int]api.Port{}
	for _, claim := range claims {
		if claim.Port < 1 || claim.Port > 65535 {
			return nil, portFailure("the guest sent an invalid port claim")
		}
		claim.Claimed, claim.Granted, claim.Reverse = true, false, false
		claim.Process, claim.Address = api.CleanText(claim.Process, 64), api.CleanText(claim.Address, 64)
		byPort[claim.Port] = claim
	}
	root.portMu.Lock()
	for port, listener := range root.portListeners {
		if listener.ctx.Err() == nil {
			entry := byPort[port]
			entry.Port, entry.Granted = port, true
			byPort[port] = entry
		}
	}
	for _, reverse := range root.reverseForwards {
		if reverse.ctx.Err() == nil && reverse.carrier != nil {
			entry := byPort[reverse.args.Port]
			entry.Port, entry.Reverse = reverse.args.Port, true
			byPort[entry.Port] = entry
		}
	}
	root.portMu.Unlock()
	ports := make([]api.Port, 0, len(byPort))
	for _, port := range byPort {
		ports = append(ports, port)
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i].Port < ports[j].Port })
	return slices.Clip(ports), nil
}

func (s *Supervisor) portForwardView() api.PortForwardViewV1 {
	s.portMu.Lock()
	defer s.portMu.Unlock()
	view := api.PortForwardViewV1{Version: 1, Forwards: []api.PortForwardInfo{}}
	for port, listener := range s.portListeners {
		if listener.ctx.Err() == nil {
			view.Forwards = append(view.Forwards, api.PortForwardInfo{Port: port})
		}
	}
	for _, reverse := range s.reverseForwards {
		if reverse.ctx.Err() == nil && reverse.carrier != nil {
			view.Forwards = append(view.Forwards, api.PortForwardInfo{Port: reverse.args.Port, Reverse: true})
		}
	}
	sort.Slice(view.Forwards, func(i, j int) bool {
		if view.Forwards[i].Port != view.Forwards[j].Port {
			return view.Forwards[i].Port < view.Forwards[j].Port
		}
		return !view.Forwards[i].Reverse && view.Forwards[j].Reverse
	})
	return view
}
