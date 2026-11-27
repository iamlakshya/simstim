package guest

import (
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

// FilesFor separates every member's holder and hook socket. IDs are validated
// before this helper is used; no request supplies a guest pathname.
func FilesFor(root, id string) host.GuestHolder {
	g := &Init{root: root}
	run := g.path(host.GuestRunDir())
	return host.GuestHolder{Sock: filepath.Join(run, "holder-"+id+".sock"), Hook: filepath.Join(run, "hook-"+id+".sock"), Manifest: filepath.Join(run, "members", id, "holder.json")}
}

func (g *Init) spawn(a proto.CtlStartArgs) (int, error) {
	if !api.ValidSessionID(a.Session) {
		return 0, execError("spawn names no valid session")
	}
	g.mu.Lock()
	if g.ended || !g.sessionPrepared && g.holder == nil || g.members[a.Session] != nil || g.memberStarts[a.Session] && !g.preparedMatchesLocked(a, proto.CtlSpawn, memberReady) || a.PreparedClaim != "" && !g.preparedMatchesLocked(a, proto.CtlSpawn, memberReady) {
		g.mu.Unlock()
		return 0, execError("the sandbox cannot start that member")
	}
	g.memberStarts[a.Session] = true
	g.mu.Unlock()
	pid, err := g.startMember(a, false)
	g.mu.Lock()
	delete(g.memberStarts, a.Session)
	if g.pendingBoot > 0 {
		g.pendingBoot--
	}
	hp := g.members[a.Session]
	g.mu.Unlock()
	if err != nil && hp != nil {
		hp.stop(0)
	}
	g.endIfEmpty()
	return pid, err
}

func (g *Init) endIfEmpty() {
	// An unwalled holder keeps its final ring and actual controller after the
	// agent exits, just as the legacy ordinary holder does. Explicit stop and
	// link loss still run the one real teardown.
	if g.unwalled != nil {
		return
	}
	g.mu.Lock()
	empty := g.emptyLocked()
	if empty {
		g.cleanStop = true
	}
	g.mu.Unlock()
	if empty {
		go g.end()
	}
}

func (g *Init) emptyLocked() bool {
	empty := g.primary != "" && !g.ended && !g.bootAdmission && len(g.memberStarts) == 0 && len(g.execs) == 0 && g.pendingBoot == 0 && g.commandHolds == 0
	for _, hp := range g.members {
		if !hp.agentEnded && !hp.exited() {
			empty = false
			break
		}
	}
	return empty
}

func stopHolders(holders []*holderProc, grace time.Duration) {
	var wg sync.WaitGroup
	for _, hp := range holders {
		wg.Add(1)
		go func() { defer wg.Done(); hp.stop(grace) }()
	}
	wg.Wait()
}
func (g *Init) holdersLocked() []*holderProc {
	out := make([]*holderProc, 0, len(g.members))
	for _, hp := range g.members {
		out = append(out, hp)
	}
	if len(out) == 0 && g.holder != nil {
		out = append(out, g.holder)
	}
	return out
}
func (g *Init) stopMember(id string) error {
	g.mu.Lock()
	hp := g.members[id]
	g.mu.Unlock()
	if hp == nil {
		return execError("that session is not a sandbox member")
	}
	if err := hp.endMember(); err != nil {
		return err
	}
	g.drainHolderEvents(hp)
	g.endIfEmpty()
	return nil
}

// plainHolderSpawnerForTests is set only by explicit _test.go setup. Neither
// a root folder nor an environment variable enables this no-wall fixture.
var plainHolderSpawnerForTests func() (*syscall.SysProcAttr, error)

// SetPlainHolderSpawnerForTest supplies the sealed test binary's plain driver.
// Production callers never set it; an unmapped production root ignores it.
func SetPlainHolderSpawnerForTest(fn func() (*syscall.SysProcAttr, error)) func() {
	old := plainHolderSpawnerForTests
	plainHolderSpawnerForTests = fn
	return func() { plainHolderSpawnerForTests = old }
}
func (g *Init) holderProcess() (*syscall.SysProcAttr, error) {
	if g.unwalled != nil {
		return g.unwalledProcessV1()
	}
	if g.vm != nil {
		return g.vm.holderProcess()
	}
	if g.root != "" && plainHolderSpawnerForTests != nil {
		return plainHolderSpawnerForTests()
	}
	return agentProcess()
}
func (g *Init) ptyABI() string {
	if g.unwalled != nil {
		return proto.GuestNonePTYV1
	}
	if g.vm != nil {
		return g.vm.ptyABI()
	}
	if g.root != "" && plainHolderSpawnerForTests != nil {
		return proto.GuestPlainTestV1
	}
	return proto.GuestPTY1000V1
}

func (hp *holderProc) endMember() error {
	if _, err := host.HolderRequestAt(hp.files.Sock, "kill", nil, time.Second); err != nil {
		return err
	}
	select {
	case <-hp.done:
		return nil
	case <-time.After(8 * time.Second):
		return execError("the member is still stopping; stop the whole sandbox before removing it")
	}
}
