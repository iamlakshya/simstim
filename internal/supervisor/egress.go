package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/wall/chv"
)

func (s *Supervisor) initEgress() error {
	if s.opt.Egress != nil {
		s.egress = s.opt.Egress
		return nil
	}
	if s.opt.EgressStarter == nil && (s.opt.EgressExe == "" || s.m.Wall == nil || (s.m.Wall.Driver != "process" && s.m.Wall.Driver != "gvisor" && s.m.Wall.Driver != chv.DriverName) || s.m.Wall.ShareNet) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.mu.Lock()
	if s.m.Wall != nil && s.m.Wall.ModelBudget > 0 && s.m.Usage == nil {
		s.m.Usage = &api.SessionUsage{Budget: s.m.Wall.ModelBudget}
	}
	initUsage := s.m.Usage.Clone()
	sessionLabel := s.id
	if s.m.Name != "" {
		sessionLabel = s.m.Name
	}
	agentKind := s.m.Agent.Kind
	policyTOML := ""
	dnsMode := egress.DNSMode("")
	if s.m.Wall != nil {
		policyTOML = s.m.Wall.PolicyTOML
		if s.m.Wall.Driver == "chv" {
			dnsMode = egress.DNSModeSynthetic
		}
	}
	s.mu.Unlock()
	args := egress.StartArgs{
		Session:       sessionLabel,
		Agent:         agentKind,
		PolicyTOML:    policyTOML,
		DNSMode:       dnsMode,
		GatewayRoutes: s.gatewayRoutes(),
		InitialUsage:  initUsage,
	}
	starter := s.opt.EgressStarter
	if starter == nil {
		starter = func(ctx context.Context, id string, args egress.StartArgs, sink egress.AuditSink) (*egress.Client, func(), error) {
			return startEgressProcess(ctx, id, s.opt.EgressExe, args, sink)
		}
	}
	client, stopFn, err := starter(ctx, s.id, args, s.recordEgressAudit)
	if err != nil {
		return err
	}
	s.egress = client
	s.egressStop = stopFn
	for _, extra := range s.m.Wall.ExtraAgents {
		s.allowAgentEgress(ctx, extra)
	}
	return nil
}

func (s *Supervisor) closeEgress() {
	s.egressMu.Lock()
	stopFn := s.egressStop
	s.egressStop = nil
	s.egressMu.Unlock()
	if stopFn != nil {
		stopFn()
	}
}

func (s *Supervisor) recordEgressAudit(ev egress.AuditEvent) {
	detail, _ := json.Marshal(ev) //nolint:gosec // G117: AuditEvent.Secret holds the secret's name only, never its value
	s.audit.Record(AuditEntry{
		Event:   "egress",
		Session: s.id,
		Detail:  string(detail),
	})
	s.handleEgressUsageAndBudget(ev)
	s.emitEgressWatchEvent(ev)
}

func startEgressProcess(ctx context.Context, id, exe string, args egress.StartArgs, sink egress.AuditSink) (*egress.Client, func(), error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("create egress socket pair: %w. Try: check open file limits on the host", err)
	}
	syscall.CloseOnExec(fds[0])
	syscall.CloseOnExec(fds[1])
	supFile := os.NewFile(uintptr(fds[0]), "egress-supervisor")
	childFile := os.NewFile(uintptr(fds[1]), "egress-child")

	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		_ = supFile.Close()
		_ = childFile.Close()
		return nil, nil, fmt.Errorf("open /dev/null for egress: %w", err)
	}

	cmd := exec.CommandContext(context.WithoutCancel(ctx), exe, "egress", id)
	cmd.Stdin = devnull
	cmd.Stdout = devnull
	cmd.Stderr = devnull
	cmd.ExtraFiles = []*os.File{childFile}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	startErr := cmd.Start()
	_ = devnull.Close()
	_ = childFile.Close()
	if startErr != nil {
		_ = supFile.Close()
		return nil, nil, fmt.Errorf("start egress process: %w. Try: check the host helper installation", startErr)
	}

	conn, err := net.FileConn(supFile)
	_ = supFile.Close()
	if err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		return nil, nil, fmt.Errorf("wrap egress link socket: %w", err)
	}

	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	stopChild := func() {
		select {
		case <-done:
			return
		default:
		}
		_ = cmd.Process.Signal(syscall.SIGTERM)
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
	}

	cl, err := egress.NewClient(ctx, conn, args, sink)
	if err != nil {
		_ = conn.Close()
		stopChild()
		return nil, nil, fmt.Errorf("init egress process: %w", err)
	}

	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = cl.Close()
			stopChild()
		})
	}
	return cl, stop, nil
}

// linkAcceptLoop accepts guest-initiated streams on s.link (such as KindEgress
// and KindDNS) and bridges them to the trusted egress process.
func (s *Supervisor) linkAcceptLoop() {
	defer s.wg.Done()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-s.doneCh:
			cancel()
		case <-s.link.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	for {
		st, err := s.link.Accept(ctx)
		if err != nil {
			return
		}
		switch st.Kind() {
		case link.KindEgress:
			s.wg.Add(1)
			go func(stream *link.Stream) {
				defer s.wg.Done()
				s.bridgeEgressStream(ctx, stream)
			}(st)
		case link.KindDNS:
			s.wg.Add(1)
			go func(stream *link.Stream) {
				defer s.wg.Done()
				s.bridgeDNSStream(ctx, stream)
			}(st)
		default:
			_ = st.Close()
		}
	}
}

func (s *Supervisor) bridgeEgressStream(ctx context.Context, guestSt *link.Stream) {
	defer func() { _ = guestSt.Close() }()
	if s.egress == nil {
		return
	}
	var hdr egress.StreamHeader
	if err := guestSt.Header(&hdr); err != nil {
		return
	}
	up, err := s.egress.OpenEgress(ctx, hdr)
	if err != nil {
		return
	}
	defer func() { _ = up.Close() }()
	spliceLinkStreams(ctx, guestSt, up)
}

func (s *Supervisor) bridgeDNSStream(ctx context.Context, guestSt *link.Stream) {
	defer func() { _ = guestSt.Close() }()
	if s.egress == nil {
		return
	}
	up, err := s.egress.OpenDNS(ctx)
	if err != nil {
		return
	}
	defer func() { _ = up.Close() }()
	spliceLinkStreams(ctx, guestSt, up)
}

func spliceLinkStreams(ctx context.Context, a, b *link.Stream) {
	stop := context.AfterFunc(ctx, func() {
		_ = a.Close()
		_ = b.Close()
	})
	defer stop()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(b, a)
		_ = b.CloseWrite()
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(a, b)
		_ = a.CloseWrite()
	}()
	wg.Wait()
}
