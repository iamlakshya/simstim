package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// testPlainProxy, when non-empty in a test, tells the guest init under the
// plain driver to start its loopback HTTP proxy at that URL (such as
// "http://127.0.0.1:0"). Production real walls always use DefaultGuestProxy.
var testPlainProxy string

// testEgressEnv holds extra environment variables passed to the egress child
// in supervisor package tests only.
var testEgressEnv []string

type egressProc struct {
	pid    int
	cmd    *exec.Cmd
	sess   *link.Session
	ctl    *egressCtl
	done   chan struct{}
	dead   atomic.Bool
	caCert string // public certificate only, received from egress on fd 3

	stopOnce sync.Once
}

type egressCtl struct {
	publicInitReady bool
	publicInit      egress.InitArgs // no values retained in this metadata projection
	acceptedKinds   map[string]bool
	pendingKinds    map[string]int
	s               *Supervisor
	st              *link.Stream
	w               *proto.Writer
	mu              sync.Mutex
	writeTurn       chan struct{}
	next            uint64
	waits           map[uint64]chan proto.Response
	done            chan struct{}
	policyMu        sync.Mutex
	events          *egressEvents
	minted          *mintedQueue
	owner           *egressProc
}

func newEgressCtl(ctx context.Context, s *Supervisor, st *link.Stream, owner *egressProc) *egressCtl {
	c := &egressCtl{
		s:      s,
		st:     st,
		w:      proto.NewWriter(st),
		waits:  map[uint64]chan proto.Response{},
		done:   make(chan struct{}),
		events: newEgressEvents(),
		minted: newMintedQueue(),
		owner:  owner,
	}
	s.wg.Add(3)
	go c.runMintedEvents(context.WithoutCancel(ctx))
	go c.runEvents(context.WithoutCancel(ctx))
	go c.closeEventControl()
	go func() {
		defer close(c.done)
		c.read()
	}()
	return c
}

func (c *egressCtl) request(ctx context.Context, op string, args, out any) (resultErr error) {
	confirmed := false
	b, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("encode egress %s: %w", op, err)
	}
	finish := c.policyTransaction(op, b)
	defer func() { finish(confirmed, resultErr) }()

	ch := make(chan proto.Response, 1)
	c.mu.Lock()
	c.next++
	id := c.next
	c.waits[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.waits, id)
		c.mu.Unlock()
	}()
	if err := c.writeControlRequest(ctx, proto.Request{ID: id, Op: op, Args: b}); err != nil {
		return fmt.Errorf("send egress %s: %w", op, err)
	}
	select {
	case resp := <-ch:
		confirmed = true
		return egressReply(op, resp, out)
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		select {
		case resp := <-ch:
			confirmed = true
			return egressReply(op, resp, out)
		default:
		}
		return errors.New("the egress control stream closed")
	}
}

func (c *egressCtl) read() {
	r := bufio.NewReaderSize(c.st, maxEvent)
	for {
		f, err := proto.Read(r)
		if err != nil {
			return
		}
		switch f.Type {
		case proto.TResponse:
			var resp proto.Response
			if json.Unmarshal(f.Payload, &resp) != nil {
				continue
			}
			c.mu.Lock()
			ch := c.waits[resp.ID]
			c.mu.Unlock()
			if ch != nil {
				select {
				case ch <- resp:
				default:
				}
			}
		case proto.TEvent:
			var ev egress.CtlEvent
			if json.Unmarshal(f.Payload, &ev) == nil {
				if ev.Event == egress.EventSecretMinted {
					if c.minted != nil && ev.Minted != nil && api.ExactJSONFields(f.Payload, egress.CtlEvent{}) {
						// Refused receipts time out only their owned response.
						c.minted.enqueue(*ev.Minted, len(f.Payload)+1)
					}
				} else if !c.events.enqueue(ev, len(f.Payload)) {
					return
				}
			}
		default:
		}
	}
}

func egressEnv() []string {
	env := []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8"}
	for _, k := range []string{"GORACE", "GOCOVERDIR"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return append(env, testEgressEnv...)
}

func guestProxyForDriver(driver string) string {
	if driver == driverPlain {
		return testPlainProxy
	}
	return host.DefaultGuestProxy
}

func sessionRefOf(m *host.Manifest) string {
	if n := strings.TrimSpace(m.Name); n != "" {
		return n
	}
	return m.ID
}

// startEgress spawns the single `<helper> egress <id>` process for this
// supervisor run over an inherited AF_UNIX socketpair on fd 3 and sends it
// the merged policy and exposed secrets. No second egress process ever starts.
func (s *Supervisor) startEgress(ctx context.Context, secrets map[string]string) (secretEnvironment, error) {
	if s.modernSecretStartup() {
		defer s.clearStartupValues(secrets)
	}
	s.mu.Lock()
	if s.egressStarted {
		s.mu.Unlock()
		return secretEnvironment{}, wallError("egress was already started for this run", nil)
	}
	s.egressStarted = true
	m := s.m
	var pol api.Policy
	if m.Policy != nil {
		pol = *m.Policy
	} else {
		merged, layers, err := egress.MergeLayers(m.Layers)
		if err != nil {
			s.mu.Unlock()
			return secretEnvironment{}, err
		}
		pol = merged
		m.Policy, m.Layers = &pol, layers
	}
	agentKind := m.Agent.Kind
	dnsMode := dnsModeForManifest(m)
	ref := sessionRefOf(m)
	s.mu.Unlock()

	exe, err := os.Executable()
	if err != nil {
		return secretEnvironment{}, wallError("cannot find the helper for egress", err)
	}
	logPath := filepath.Join(paths.SessionDir(s.id), "egress.log")
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return secretEnvironment{}, wallError("cannot open the egress log", err)
	}
	defer logf.Close()

	ours, theirs, err := guest.SocketPair()
	if err != nil {
		return secretEnvironment{}, wallError("cannot create the egress socket pair", err)
	}
	defer theirs.Close()

	cmd := exec.CommandContext(context.WithoutCancel(ctx), exe, "egress", s.id)
	cmd.Env = egressEnv()
	cmd.ExtraFiles = []*os.File{theirs}
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.WaitDelay = time.Second
	cmd.SysProcAttr = host.StopWithParent(&syscall.SysProcAttr{Setsid: true})
	if err := ctx.Err(); err != nil {
		_ = ours.Close()
		return secretEnvironment{}, wallError("the boot ended before egress started", err)
	}
	if err := cmd.Start(); err != nil {
		_ = ours.Close()
		return secretEnvironment{}, wallError("cannot start the egress process", err)
	}

	sess := link.New(ours, link.Supervisor, egress.LinkOptions())
	ep := &egressProc{
		pid:  cmd.Process.Pid,
		cmd:  cmd,
		sess: sess,
		done: make(chan struct{}),
	}
	s.mu.Lock()
	s.eg = ep
	s.mu.Unlock()

	go s.watchEgress(ep)

	ctlSt, err := sess.Open(ctx, link.KindCtl, nil)
	if err != nil {
		s.stopEgress()
		return secretEnvironment{}, wallError("the egress process did not answer on its link", err)
	}
	ep.ctl = newEgressCtl(ctx, s, ctlSt, ep)
	kinds := host.MemberAgentKinds(s.m, s.knownMembers)
	initArgs := egress.InitArgs{
		AgentKinds: kinds,
		SessionID:  s.id,
		SessionRef: ref,
		Agent:      agentKind,
		DNS:        dnsMode,
		Policy:     pol,
		Secrets:    secrets,
	}
	ep.ctl.setPublicInitial(initArgs) // before any init/member event or first write
	environment, err := s.initialiseEgress(ctx, ep, initArgs, secrets)
	if err != nil {
		s.stopEgress()
		return secretEnvironment{}, err
	}
	s.mu.Lock()
	s.memberReady = true
	s.mu.Unlock()
	return environment, nil
}

func (s *Supervisor) watchEgress(ep *egressProc) {
	_ = ep.cmd.Wait()
	ep.dead.Store(true)
	_ = ep.sess.Close()
	close(ep.done)

	s.mu.Lock()
	current := s.eg == ep
	unexpected := current && !s.egressStopping && s.rec.phase == host.LifeRunning
	var snap host.Manifest
	if current && s.m.Armed {
		s.m.Armed = false
		s.dirty = true
	}
	if current && disarmPublicSecretsLocked(s.m) {
		s.dirty = true
	}
	if unexpected {
		s.raise(s.alertEvent(alerts.SandboxError))
		snap = s.snapshot()
	}
	s.mu.Unlock()

	if unexpected {
		s.log.Error("the egress process exited while the session ran; new flows fail closed", "pid", ep.pid)
		s.audit.write("egress_died", map[string]any{"pid": ep.pid})
		_ = s.save(&snap, false)
	}
}

func (s *Supervisor) stopEgress() {
	s.mu.Lock()
	s.egressStopping = true
	s.m.Armed = false
	if disarmPublicSecretsLocked(s.m) {
		s.dirty = true
	}
	ep := s.eg
	s.mu.Unlock()
	if ep == nil {
		return
	}
	ep.stopOnce.Do(func() {
		_ = ep.sess.Close()
		if !ep.dead.Load() {
			_ = ep.cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-ep.done:
				return
			case <-time.After(2 * time.Second):
			}
			_ = ep.cmd.Process.Kill()
		}
		select {
		case <-ep.done:
		case <-time.After(reapWait):
		}
	})
	if ep.ctl != nil {
		<-ep.ctl.events.finished
		<-ep.ctl.events.controlClosed
		if ep.ctl.minted != nil {
			<-ep.ctl.minted.finished
		}
	}
}

// acceptGuestStreams forwards KindEgress and KindDNS streams opened by the
// guest init on s.link to the session's egress process. When egress has died,
// every new stream fails closed at once and no second egress process starts.
func (s *Supervisor) acceptGuestStreams() {
	defer s.wg.Done()
	egressSem := make(chan struct{}, link.DefaultMaxGuestStreams)
	dnsSem := make(chan struct{}, egress.MaxActiveDNS)
	for {
		st, err := s.link.Accept(context.Background())
		if err != nil {
			return
		}
		switch st.Kind() {
		case link.KindRPort:
			s.forwardReverseStream(st)
		case link.KindEgress:
			select {
			case egressSem <- struct{}{}:
				go func(guestSt *link.Stream) {
					defer func() { <-egressSem }()
					s.forwardEgressStream(guestSt)
				}(st)
			default:
				_ = st.Close()
			}
		case link.KindDNS:
			select {
			case dnsSem <- struct{}{}:
				go func(guestSt *link.Stream) {
					defer func() { <-dnsSem }()
					s.forwardDNSStream(guestSt)
				}(st)
			default:
				_ = st.Close()
			}
		default:
			_ = st.Close()
		}
	}
}

func (s *Supervisor) forwardEgressStream(guestSt *link.Stream) {
	defer guestSt.Close()
	s.mu.Lock()
	ep := s.eg
	s.mu.Unlock()
	if ep == nil || ep.dead.Load() {
		return
	}
	var raw json.RawMessage
	if err := guestSt.Header(&raw); err != nil {
		return
	}
	hdr, err := egress.ParseStreamHeader(raw)
	if err != nil {
		return
	}
	hdr.Kind = ""

	ctx, cancel := context.WithTimeout(context.Background(), streamTimeout)
	egSt, err := ep.sess.Open(ctx, link.KindEgress, hdr)
	cancel()
	if err != nil {
		return
	}
	defer egSt.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(egSt, guestSt)
		_ = egSt.CloseWrite()
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(guestSt, egSt)
		_ = guestSt.CloseWrite()
	}()
	wg.Wait()
}
