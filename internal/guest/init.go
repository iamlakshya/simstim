// Package guest is the guest init: the host helper as the first process
// inside a walled session's sandbox, `<helper> wall-init` (spec 4 and 5).
// It serves its end of the link to the supervisor, starts the session's
// holder when the supervisor asks, and carries each holder stream to the
// holder's socket, byte for byte.
//
// Everything here runs inside the wall and is untrusted. The supervisor
// checks each frame that comes out, and keeps what the guest reports
// apart from its own facts, as claims.
package guest

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/secretmem"
)

// RootEnv names a folder that stands for the sandbox's root, for the
// guest's own folders: /run/<name> and /home/agent. Only the test driver
// plain sets it. It builds no mount namespace, so those folders live in
// that folder instead, and the guest init maps each path under them.
var RootEnv = brand.Env("GUEST_ROOT")

// linkFD is the fd of the link that the guest init inherits: one end of a
// socket pair, so that nothing listens (spec 3.3, G7).
const linkFD = 3

// heartbeatEvery is how often the guest init says that it is alive.
const heartbeatEvery = 5 * time.Second

// Init is the guest init of one sandbox.
type Init struct {
	purgeReader             purgeReaderHooksV1
	unwalled                *unwalledGuestV1
	sshAgent                *SSHAgentProxy
	imageSaves              map[string]*imageSaveGuestV1
	packageSettingsRequired bool // set only by the actual closed walled constructors
	packageSettingsPhase    packageSettingsPhase
	packageSettingsPlan     *proto.PackageManagersV1
	decoysApplied           bool
	decoyGeneration         *proto.DecoyFilesV1
	portWG                  sync.WaitGroup
	reversePorts            map[string]*reverseListener
	copySlots               chan struct{}
	fileWG                  sync.WaitGroup
	lifetime                context.Context
	cancelLifetime          context.CancelFunc
	preparedMembers         map[string]*preparedMember
	secretStores            *secretStoreOwnership
	inputs                  *inputPhase
	inputStart              *proto.CtlStartArgs
	inputEnv                []string
	vm                      vmHooks
	sessionPrepared         bool
	bundles                 map[string]*bundleOperation
	dnsErr                  error
	proxy                   *Proxy
	dns                     *DNS
	dnsSocks                DNSSockets
	owned                   map[*execRun]struct{}
	commandHolds            int
	caReady                 bool
	members                 map[string]*holderProc
	memberStarts            map[string]bool
	primary                 string
	pendingBoot             int
	bootAdmission           bool // pending transaction ready, not an invented member
	execs                   map[string]*execRun
	execEnv                 []string
	execCwd                 string
	stateFiles              []string
	captureNonce            string
	stateSession            string
	cleanStop               bool
	seedArgs                *proto.CtlSeedArgs
	seedDone                chan error
	seedStreams             chan *link.Stream
	seedTaken               bool
	root                    string
	log                     *slog.Logger
	sess                    *link.Session

	// reaper is true for the guest init of a sandbox (Run): on Linux it is
	// a child subreaper, and its end reaps every child that it has
	// (reap_linux.go). A guest init in a test's own process reaps nothing.
	reaper bool

	mu       sync.Mutex
	ctl      *proto.Writer // the ctl stream, once the supervisor opened it
	ctlSt    *link.Stream
	holder   *holderProc   // the session's holder, once it started
	starting bool          // a start runs, and answers for the holder's end
	ended    bool          // the end has begun: no holder starts now
	done     chan struct{} // closed when the guest init ends
	once     sync.Once
}

// Run is the guest init. It serves the link on fd 3 until the supervisor
// stops it, or the link ends.
func Run(args ...string) error {
	unwalledID := ""
	if len(args) == 3 && args[0] == "--wall" && args[1] == "none" {
		unwalledID = args[2]
		args = nil
	}
	if len(args) != 0 {
		if len(args) == 1 && args[0] == "--self-test" {
			return runSelfTest(os.Stdout, os.Stderr)
		}
		if len(args) == 1 && args[0] == "--microvm-v1" {
			return runMicroVM()
		}
		if len(args) == 1 && args[0] == purgeReaderArgumentV1 {
			return runPurgeReaderVMV1()
		}
		return vmError("unknown versioned guest init argument")
	}
	if err := secretmem.Protect(); err != nil {
		return vmError("the guest init cannot protect its private control input")
	}
	var dnsSocks DNSSockets
	var dnsErr error
	if unwalledID == "" {
		dnsSocks, dnsErr = claimInheritedDNSSockets()
	}
	f := os.NewFile(linkFD, "link")
	if f == nil {
		return &proto.Error{Code: "wall_init", Message: "the guest init has no link on fd 3",
			Hint: "Only a supervisor starts the guest init, with the link on fd 3."}
	}
	conn, err := net.FileConn(f)
	f.Close()
	if err != nil {
		return &proto.Error{Code: "wall_init", Message: "the guest init cannot use its link: " + err.Error(),
			Hint: "Only a supervisor starts the guest init, with the link on fd 3."}
	}
	// Its log line on a signal never ends it before its end ran, whoever
	// reads its output.
	host.KeepOnBrokenPipe()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	var mode *unwalledGuestV1
	var earlyReaper bool
	var sig chan os.Signal
	if unwalledID != "" {
		mode, err = newUnwalledGuestV1(unwalledID)
		if err != nil {
			conn.Close()
			return err
		}
		earlyReaper = becomeSubreaper(logger)
		if !earlyReaper {
			// No hello or controller link is handed back before the actual
			// None creator can join its original descendants. This remains
			// an init-only failed preparation with no user process started.
			cleanup := errors.Join(mode.runtime.Close(), conn.Close())
			if mode.helper != nil {
				cleanup = errors.Join(cleanup, mode.helper.Close())
			}
			return errors.Join(&proto.Error{Code: "wall_init", Message: "the unwalled init cannot become a native child subreaper", Hint: "Use a Linux host which permits unprivileged child subreapers, then retry the original start."}, cleanup)
		}
		sig = make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	}
	g := New(conn, os.Getenv(RootEnv), logger)
	g.packageSettingsRequired = true
	if unwalledID != "" {
		g.unwalled = mode
		g.root = ""
		g.packageSettingsRequired = false
	} else {
		if g.root != "" && plainHolderSpawnerForTests != nil {
			g.packageSettingsRequired = false
		}
		g.setInheritedDNS(dnsSocks, dnsErr)
	}
	if mode != nil {
		g.reaper = earlyReaper
	} else {
		g.reaper = becomeSubreaper(g.log)
	}
	// A stop, from the test driver or as the supervisor dies
	// (host.StopWithParent), ends the sandbox from the inside first.
	if sig == nil {
		sig = make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	}
	defer signal.Stop(sig)
	go func() {
		select {
		case s := <-sig:
			g.log.Info("stopping on a signal", "signal", s.String())
			g.end()
		case <-g.done:
		}
	}()
	if g.reaper {
		go g.reapLoop()
	}
	g.Serve()
	g.end() // Join the same once owner before observing its cleanup result.
	if g.unwalled != nil {
		return g.unwalled.cleanupErr
	}
	return nil
}

// New starts the guest's end of the link over conn. root is the folder
// that stands for the sandbox's root, or "" in a real wall.
func New(conn net.Conn, root string, log *slog.Logger) *Init {
	lifetime, cancel := context.WithCancel(context.Background())
	return &Init{lifetime: lifetime, cancelLifetime: cancel, root: root, log: log, sess: link.New(conn, link.Guest, link.Options{}), done: make(chan struct{}), members: make(map[string]*holderProc), memberStarts: make(map[string]bool), seedStreams: make(chan *link.Stream, 1)}
}

// Serve takes the streams that the supervisor opens, until the link ends
// or a stop has run. It then stops the holder.
func (g *Init) Serve() {
	defer g.end()
	for {
		st, err := g.sess.Accept(context.Background())
		if err != nil {
			g.log.Info("the link ended", "err", err)
			return
		}
		if g.vm != nil && st.Kind() != link.KindCtl && !g.vm.prepared() {
			_ = st.Close()
			continue
		}
		if g.purgeReader != nil && st.Kind() != link.KindCtl {
			_ = st.Close()
			continue
		}
		switch st.Kind() {
		case link.KindSFTP:
			go g.serveEditorSFTPV1(st)
		case link.KindPort:
			g.acceptPort(st)
		case link.KindCtl:
			go g.serveCtl(st)
		case link.KindLayer:
			g.acceptImageSaveLayerV1(st)
		case link.KindGet:
			g.acceptCopy(st)
		case link.KindPut:
			if !g.acceptInputStream(st) {
				g.acceptPut(st)
			}
		case link.KindBundle:
			if g.acceptInputStream(st) {
				continue
			}
			if g.tryBundleCommand(st) {
				continue
			}
			g.mu.Lock()
			active := g.seedArgs != nil && !g.seedTaken && !g.ended
			if active {
				g.seedTaken = true
			}
			g.mu.Unlock()
			if active {
				select {
				case g.seedStreams <- st:
				default:
					_ = st.Close()
				}
			} else {
				_ = st.Close()
			}
		case link.KindExec:
			go g.serveExec(st)
		case link.KindHolder:
			if !g.tryEditorTerminalV1(st) {
				go g.serveHolder(st)
			}
		default:
			// Later versions carry the other kinds. Refuse them now.
			_ = st.Close()
		}
	}
}

// end stops the holder, ends every other child that the guest init has,
// and closes the link, once. No holder starts after it began.
func (g *Init) end() {
	g.once.Do(func() {
		g.mu.Lock()
		g.ended = true
		if g.cancelLifetime != nil {
			g.cancelLifetime()
		}
		holders, st, clean := g.holdersLocked(), g.ctlSt, g.cleanStop
		proxy, dns := g.proxy, g.dns
		dnsSocks := g.dnsSocks
		g.dnsSocks = DNSSockets{}
		g.mu.Unlock()
		if g.vm != nil {
			g.vm.beginStop()
		}
		g.stopPorts()
		g.stopInputs()
		g.stopBundles()
		if err := g.stopImageSavesV1(); err != nil {
			clean = false
			g.log.Error("original image export cleanup failed", "err", err)
			if original, ok := g.vm.(interface{ setError(error) }); ok {
				original.setError(err)
			}
		}
		g.stopExecs()
		g.stopSSHAgent()
		g.fileWG.Wait()
		if g.vm != nil {
			clean = g.vm.joinPTY() && clean
		}
		stopHolders(holders, 3*time.Second)
		if g.reaper {
			// The holder is reaped: only this loop reaps now.
			joined := g.reapOrphans()
			clean = joined && clean
			if g.unwalled != nil && !joined {
				g.unwalled.cleanupErr = execError("the unwalled init did not confirm original child and terminal joins")
			}
		} else if g.unwalled != nil {
			g.unwalled.cleanupErr = execError("the unwalled init has no native subreaper completion producer")
		}
		var drains sync.WaitGroup
		for _, hp := range holders {
			drains.Add(1)
			go func() { defer drains.Done(); g.drainHolderEvents(hp) }()
		}
		drains.Wait()
		if g.vm != nil {
			if err := g.vm.cleanupFiles(); err != nil {
				clean = false
				g.log.Error("guest filesystem cleanup failed", "err", err)
			}
		}
		g.captureState(clean && g.reaper)
		if g.vm != nil {
			if err := g.vm.shutdown(); err != nil {
				g.log.Error("guest VM cleanup failed", "err", err)
			}
		}
		if st != nil {
			// The answers and events that wait go out before the link
			// closes. Bound Close too: its normal linger is five seconds.
			_ = st.SetWriteDeadline(time.Now().Add(ctlCloseWait))
			_ = st.Close()
		}
		_ = dnsSocks.Close()
		if proxy != nil {
			_ = proxy.Close()
		}
		if dns != nil {
			_ = dns.Close()
		}
		_ = g.sess.Close()
		for _, hp := range holders {
			hp.closeEvents()
			// Closing both the event socket and link releases a read as
			// well as a forwarder waiting for ctl stream credit.
			<-hp.eventsDone
		}
		if g.unwalled != nil {
			if err := g.unwalled.runtime.Close(); err != nil {
				g.unwalled.cleanupErr = errors.Join(g.unwalled.cleanupErr, err)
			}
			if g.unwalled.helper != nil {
				if err := g.unwalled.helper.Close(); err != nil {
					g.unwalled.cleanupErr = errors.Join(g.unwalled.cleanupErr, err)
				}
			}
		}
		close(g.done)
	})
}

// Done is closed when the guest init has ended.
func (g *Init) Done() <-chan struct{} { return g.done }

// serveCtl serves the ctl stream: the supervisor's ops, and the guest's
// events and heartbeats.
func (g *Init) serveCtl(st *link.Stream) {
	w := proto.NewWriter(st)
	g.mu.Lock()
	if g.ctlSt != nil && g.ctlSt != st {
		g.mu.Unlock()
		_ = st.Close()
		return
	}
	g.ctl, g.ctlSt = w, st
	g.mu.Unlock()
	go g.heartbeat(w)
	r := bufio.NewReader(st)
	for {
		f, err := proto.ReadLimit(r, maxEvent)
		if err != nil {
			g.end()
			return
		}
		if f.Type != proto.TRequest {
			continue
		}
		var req proto.Request
		if json.Unmarshal(f.Payload, &req) != nil {
			continue
		}
		g.request(w, req)
	}
}

// stop stops the holder within grace, then ends the guest init.
func (g *Init) stop(grace time.Duration) {
	if g.vm != nil {
		g.vm.stop(grace)
		return
	}
	g.mu.Lock()
	holders := g.holdersLocked()
	g.mu.Unlock()
	stopHolders(holders, grace)
	g.end()
}

// path is the guest's path p, under the root that stands for the
// sandbox's root, when there is one.
func (g *Init) path(p string) string {
	if g.unwalled != nil {
		if p == host.GuestHome {
			return g.unwalled.home
		}
		if p == host.GuestRunDir() {
			return g.unwalled.runtime.Name()
		}
		if strings.HasPrefix(p, host.GuestRunDir()+"/") {
			return filepath.Join(g.unwalled.runtime.Name(), strings.TrimPrefix(p, host.GuestRunDir()+"/"))
		}
		return p
	}
	if g.root == "" {
		return p
	}
	return filepath.Join(g.root, p)
}

// mapValue maps a path in the guest's own folders, such as a variable's
// value, under the root that stands for the sandbox's root. Other values
// stay as they are.
func (g *Init) mapValue(v string) string {
	if g.root == "" {
		return v
	}
	for _, dir := range []string{host.GuestHome, host.GuestRunDir()} {
		if v == dir || strings.HasPrefix(v, dir+"/") {
			return filepath.Join(g.root, v)
		}
	}
	return v
}

// mapEnv maps each variable's value as mapValue does.
func (g *Init) mapEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		out = append(out, k+"="+g.mapValue(v))
	}
	return out
}
