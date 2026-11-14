// Package supervisor is the trusted side of one walled session (spec 5.3).
// It runs outside the wall, as `<helper> supervise <id>`, the main process
// of the session's unit. It builds the sandbox with its driver, serves the
// session's socket, and passes each connection to the holder inside the
// wall over the link. It keeps the trusted manifest, the audit log, the
// alerts and the memory facts.
//
// The supervisor never runs, renders or interpolates what the guest sends.
// It parses only link frames, the type and size of each holder frame that
// it passes, the small end and error frames, a holder's answer to a request,
// and the JSON of ctl events, each capped at 64 KiB. It never reads an
// output frame's payload. Every string that it keeps from a claim goes
// through api.CleanText first. A claim never becomes a trusted fact: the
// supervisor keeps its own record of the run and its end (life.go).
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/phaseclock"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/secretmem"
	"github.com/iamlakshya/simstim/internal/version"
	"github.com/iamlakshya/simstim/internal/vmconfig"
)

// bootTimeout bounds the sandbox's start, from its build to the agent's.
// Tests shorten it.
var bootTimeout = 30 * time.Second

// now is the supervisor's clock, for the heartbeat's silence. Tests
// replace it.
var now = func() time.Time { return phaseclock.Current.Now() }

// testStarted, when set, gets each supervisor once its session has
// started. Only tests set it.
var testStarted func(*Supervisor)

// notifySignals relays the signals that stop the session with the host,
// and stopSignals stops that. Tests replace them, to send a signal without
// signalling the test.
var (
	notifySignals = func(c chan<- os.Signal) { signal.Notify(c, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP) }
	stopSignals   = func(c chan<- os.Signal) { signal.Stop(c) }
)

// errStopped is a boot that a stop with the host ended. Restore starts the
// session again.
var errStopped = &proto.Error{Code: "stopped", Message: "the host stopped the session while its sandbox started",
	Hint: "Restore the session after the host starts again, or resume it."}

// Supervisor holds one walled session's trusted state.
type Supervisor struct {
	diskWatcher           *diskWatcherV1
	sandboxStartAttempted bool
	bootCancel            context.CancelCauseFunc
	bootContextGeneration string
	diskFailure           error
	diskAlerted           bool
	lateInput             *startupInput
	bootBudget            *activeBootBudget
	storeBatch            *trustedStoreBatch
	memberStores          map[string]bool
	memberBootPlans       []memberBootPlan
	startupInput          *startupInput
	startupErr            error
	runtimeCleaned        chan struct{}
	runtimeCleanup        sync.Once
	runtimeCleanupErr     error
	memberValues          map[string]map[string]string
	startupSecrets        *startupSecretInput
	retainedSecrets       *api.SessionSecretState
	automatic             *host.AutomaticRestorePlan
	bootOperation         *host.SessionOperation
	knownMembers          []*host.Manifest
	terminalABI           string
	vmPlan                *vmconfig.Plan
	eg                    *egressProc
	polMu                 sync.Mutex
	egressStarted         bool
	egressStopping        bool
	memberReady           bool
	loadedName            string
	firstSaved            bool
	lifetime              context.Context
	generation            string
	runToken              string
	launched              bool
	launches              map[string]*Supervisor
	expected              map[string]bool
	memberFault           bool
	stopIntent            bool // controller Stop/System, separate from natural exit
	savedMembers          []*host.Manifest
	primary               *Supervisor
	members               map[string]*Supervisor
	membersMu             sync.Mutex
	spawnMu               sync.Mutex
	capture               *files.Capture
	captureReady          string
	captureFailed         bool
	id                    string
	log                   *slog.Logger
	audit                 *auditLog
	sb                    sandbox
	link                  *link.Session
	ctl                   *ctlConn
	ln                    net.Listener
	peerCheck             bool
	al                    *alerts.Dispatcher
	stop                  chan struct{}
	quit                  chan string    // a reason to stop, from a kill
	sig                   chan os.Signal // a stop with the host
	wg                    sync.WaitGroup

	mu       sync.Mutex
	m        *host.Manifest // the trusted manifest
	rec      record         // the record of the run (life.go)
	dirty    bool
	leader   *client // the client whose size the agent's terminal has
	turn     turnClock
	mark     memMark
	lastBeat time.Time
	silent   bool   // the guest init has missed its heartbeats
	lost     string // why the link was lost, when the supervisor closed it
	end      endFacts
	saveMu   sync.Mutex
	closed   bool // the last save is done

	// Events from the guest, in order (claims.go): ready is true once the
	// run is recorded, and early holds the events that came before, at
	// most maxEarlyBytes of them, and the first valid end beyond that.
	evMu         sync.Mutex
	ready        bool
	early        [][]byte
	earlyBytes   int
	earlyEnd     bool
	earlyCapture bool

	// Large frames in flight across all holder streams (relay.go).
	relayMu   sync.Mutex
	relayUsed int64
	relayWake chan struct{}
}

// Run supervises session id: it builds the sandbox, serves the session's
// socket, and returns when the sandbox has ended and the session is stopped,
// ended or killed.
func runSupervisor(owner context.Context, id string) error {
	return runSupervisorMode(owner, id, nil)
}

func runSupervisorMode(owner context.Context, id string, operation *host.SessionOperation) (returned error) {
	if err := secretmem.Protect(); err != nil {
		return err
	}
	lifetime, cancelLifetime := context.WithCancel(owner)
	defer cancelLifetime()
	release, err := host.AcquireSupervisorLease(id)
	if err != nil {
		return wallError("cannot claim the supervisor process", err)
	}
	defer release()
	m, err := host.LoadManifest(id)
	if err != nil {
		return &proto.Error{Code: "not_found", Message: "the supervisor cannot read session " + id + ": " + err.Error(),
			Hint: "List the host's sessions with " + brand.HostBinary + " ls, then resume the session."}
	}
	if err := knownSessionSecretState(m.SecretState); err != nil {
		return err
	}
	if err := host.ValidateManifestStart(m); err != nil {
		return err
	}
	if m.Wall == nil {
		return &proto.Error{Code: "not_walled", Message: "session " + id + " has no wall, so it has no supervisor",
			Hint: "Resume the session: the host starts its holder."}
	}
	if m.SandboxOf != "" {
		return execRefusal("a sandbox member cannot launch a separate supervisor")
	}
	var automatic *host.AutomaticRestorePlan
	if operation != nil {
		if !operation.Valid(id) || operation.AutomaticBoot() == nil {
			return pullChanged()
		}
		automatic, err = host.ValidateAutomaticRestore(m)
		if err != nil {
			return err
		}
	} else if len(m.AutomaticRestore) > 0 {
		return &proto.Error{Code: "restore_plan", Message: "this sandbox needs its closed automatic controller entrypoint", Hint: "Use the current helper's canonical restore command."}
	}
	lf, err := os.OpenFile(paths.SupervisorLog(id), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return &proto.Error{Code: "start_failed", Message: "the supervisor cannot open its log: " + err.Error(),
			Hint: "Check the host's free space and the session's folder, then resume the session."}
	}
	defer lf.Close()
	s := &Supervisor{automatic: automatic, bootOperation: operation, runtimeCleaned: make(chan struct{}), loadedName: m.Name, lifetime: lifetime, id: id, m: m, log: slog.New(slog.NewTextHandler(lf, nil)), quit: make(chan string, 1), stop: make(chan struct{}, 1),
		sig: make(chan os.Signal, 1), peerCheck: host.PeerCheck(id)}
	generation, err := newRunToken()
	if err != nil {
		return wallError("cannot make the sandbox run identity", err)
	}
	s.generation = generation
	s.runToken = generation + "/" + id
	if err := s.loadSavedMembers(); err != nil {
		return err
	}
	s.retainedSecrets, err = collectSavedSecretState(lifetime, *m, s.knownMembers)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, s.closeLateInput()) }()
	notifySignals(s.sig)
	defer stopSignals(s.sig)
	s.log.Info("supervisor starting", "session", id, "driver", m.Wall.Driver, "version", version.Version)
	s.audit = openAudit(paths.AuditLog(id), s.log)
	defer s.audit.close()
	// Alert commands run in the supervisor's own environment, never the
	// agent's, which is the guest's (docs/how-to/alerts.md). Under systemd,
	// that is the user manager's environment.
	s.al = alerts.New(alerts.Options{Env: os.Environ(), Dir: homeDir(), Logger: s.log})
	// The record begins before the socket listens, and is saved after it:
	// New waits for the socket, then reads the record, and dials the
	// socket to see that the supervisor still runs (host.waitBoot).
	wait := s.bootLimit()
	if (len(m.Secrets) > 0 || m.Inputs != nil || m.Run.StartEnvelope) && !host.WasRestored(m) {
		wait += secretsWait
	}
	s.mu.Lock()
	s.rec = record{phase: host.LifeBooting, bootBy: now().Add(wait)}
	snap := s.snapshot()
	s.mu.Unlock()
	agentsReceipt, err := s.prepareBootAgents(s.lifetimeContext())
	if err != nil {
		return s.failed(host.EndBoot, err)
	}
	defer func() {
		if agentsReceipt != nil {
			returned = errors.Join(returned, agentsReceipt.Close())
		}
	}()
	sb, err := s.sandboxForRun()
	if err == nil {
		s.adoptFileCapture(sb)
	}
	if err != nil {
		return s.failed(host.EndBoot, err)
	}
	s.mu.Lock()
	s.sb = sb
	s.mu.Unlock()
	defer func() {
		stopErr := s.stopDiskWatcherV1()
		closeErr := s.closeDiskResourcesV1()
		returned = errors.Join(returned, stopErr, closeErr)
	}()
	if err := s.listen(); err != nil {
		return s.failed(host.EndBoot, errors.Join(err, s.closeSandbox()))
	}
	if err := s.save(&snap, false); err != nil {
		return s.failed(host.EndBoot, errors.Join(err, s.closeSandbox()))
	}
	if err := s.boot(sb); err != nil {
		// Every end runs the sandbox's cleanup: a boot that failed or that
		// a stop cut short too.
		cleanupErr := s.closeSandbox()
		kind := host.EndBoot
		if errors.Is(err, errStopped) {
			kind = host.EndSystem
		}
		return s.failed(kind, errors.Join(err, cleanupErr))
	}
	if agentsReceipt != nil {
		if err := agentsReceipt.Close(); err != nil {
			return s.failed(host.EndBoot, errors.Join(err, s.closeSandbox()))
		}
		agentsReceipt = nil
	}
	if operation != nil {
		operation.Close() // all actual selected children and ready commit completed
	}
	s.wg.Add(3)
	go s.acceptGuestStreams()
	go s.acceptLoop()
	go s.tick()
	if testStarted != nil {
		testStarted(s)
	}
	s.wait()
	return nil
}

// started records the run in the trusted manifest, writes the audit line
// start, and raises started or restored.
func (s *Supervisor) started(res proto.CtlStartResult, missing []string) error {
	restored := host.WasRestored(s.m)
	t := now().UTC()
	s.mu.Lock()
	s.launched = true
	s.setPhase(host.LifeRunning)
	s.rec.started = t
	m := s.m
	if u := host.OwnUnit(s.id); u != "" {
		m.Run.Unit = u
	}
	m.Run.HolderPID = os.Getpid()
	m.Run.AgentPID = 0 // the guest's process IDs are claims (m.Guest)
	m.Run.Socket = paths.Socket(s.id)
	m.Run.BootID = host.BootID()
	m.Run.StartEnvelope = false
	m.Run.StartedAt = t
	m.Run.Generation = s.generation
	if exe, err := os.Executable(); err == nil {
		m.HolderBinary = exe
	}
	m.HostVersion = version.Version
	m.DebugUntil = nil
	m.MissingSecrets = missing
	root := s.owner()
	m.Armed = root.eg != nil && !root.eg.dead.Load()
	m.Guest = &host.GuestClaims{AgentPID: max(res.AgentPID, 0), At: t}
	m.Status = host.Status{State: host.StateWorking, Since: t, Source: "start"}
	s.lastBeat = now()
	s.turn.since = now() // a session starts out working
	kind := alerts.Started
	if restored {
		kind = alerts.Restored
	}
	snapshot := s.snapshot()
	s.mu.Unlock()
	if err := s.save(&snapshot, false); err != nil {
		return err
	}
	s.raise(s.alertEvent(kind))
	s.registerLaunch()
	s.replay()
	s.audit.write("start", map[string]any{"level": snapshot.Wall.Level, "driver": snapshot.Wall.Driver, "version": version.Version})
	s.log.Info("the session started", "unit", snapshot.Run.Unit, "peer_check", s.peerCheck)
	return nil
}

// snapshot is a copy of the trusted manifest, to save or send after the
// caller lets s.mu go, with the record of the run as its Life. It copies
// the claims too: claims change in place. The caller holds s.mu.
func (s *Supervisor) snapshot() host.Manifest {
	m := host.CloneManifest(*s.m)
	if m.Guest != nil {
		g := *m.Guest
		m.Guest = &g
	}
	m.Life = s.rec.life()
	return m
}

// save writes m, a copy of the trusted manifest, unless the last save has
// been made. After it, Delete may remove the session's folder at any
// moment, and a late save would bring it back.
func (s *Supervisor) save(m *host.Manifest, last bool) error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	return s.saveLocked(m, last)
}

func (s *Supervisor) saveLocked(m *host.Manifest, last bool) error {
	if s.closed {
		return nil
	}
	if err := s.writeLive(m, last); err != nil {
		s.log.Warn("manifest not saved", "err", err)
		return fmt.Errorf("save the trusted manifest: %w", err)
	}
	return nil
}

// tick saves the trusted manifest when it changed, reads the unit's memory
// and watches the guest init's heartbeat, once a second, until the link
// ends.
func (s *Supervisor) tick() {
	defer s.wg.Done()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
		case <-s.link.Done():
			return
		}
		s.saveMembers()
		s.memoryTick()
		s.watchBeat()
		if n := s.ctl.drops(time.Now()); n > 0 {
			s.audit.write("events_dropped", map[string]any{"count": n})
		}
		s.mu.Lock()
		if s.m.Status.State == host.StateWorking {
			// The agent works: the host is not idle (idle.go).
			s.active(time.Now())
		}
		dirty := s.dirty
		s.dirty = false
		m := s.snapshot()
		s.mu.Unlock()
		if dirty {
			_ = s.save(&m, false)
		}
	}
}

// active records activity in the session at t, at most once a second.
// The caller holds s.mu.
func (s *Supervisor) active(t time.Time) {
	if t.Sub(s.m.LastActivityAt) >= time.Second {
		s.m.LastActivityAt = t.UTC()
		s.dirty = true
	}
}

// orDefault is n, or def when n is not positive.
func orDefault(n, def int) int {
	if n <= 0 {
		return def
	}
	return n
}

// homeDir is the user's home, where alert commands run.
func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}
