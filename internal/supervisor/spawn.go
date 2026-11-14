package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/phaseclock"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (s *Supervisor) owner() *Supervisor {
	if s.primary != nil {
		return s.primary
	}
	return s
}
func (s *Supervisor) member(id string) *Supervisor {
	root := s.owner()
	if id == root.id {
		return root
	}
	root.membersMu.Lock()
	defer root.membersMu.Unlock()
	return root.members[id]
}
func (s *Supervisor) childList() []*Supervisor {
	root := s.owner()
	root.membersMu.Lock()
	defer root.membersMu.Unlock()
	out := make([]*Supervisor, 0, len(root.members))
	for _, child := range root.members {
		out = append(out, child)
	}
	slices.SortFunc(out, func(a, b *Supervisor) int { return strings.Compare(a.id, b.id) })
	return out
}

func (s *Supervisor) spawn(a api.JoinArgs) (*api.SessionInfo, error) {
	root := s.owner()
	root.spawnMu.Lock()
	defer root.spawnMu.Unlock()
	root.mu.Lock()
	running := root.rec.phase == host.LifeRunning
	parent := root.snapshot()
	root.mu.Unlock()
	if !running {
		return nil, execRefusal("the sandbox is stopped or is still starting")
	}
	if err := root.requireConfirmedStoreBatch(); err != nil {
		return nil, err
	}
	budget := newActiveBootBudget(root.lifetimeContext(), bootTimeout)
	defer budget.close()
	ctx := context.Context(budget)
	if len(a.Secrets) > 0 || a.SecretDelivery != nil && len(a.SecretDelivery.Modes) > 0 {
		if parent.SecretState != nil && (a.SecretGeneration == "" || a.SecretGeneration != parent.SecretState.Generation) {
			return nil, secretIntentFailure()
		}
		if a.SecretDelivery != nil && parent.SecretState == nil {
			return nil, secretNextStartAuthority()
		}
	}
	if a.Name == "" {
		a.Name = root.memberName(a.Agent)
	}
	prepared, err := host.PrepareSandboxMember(&parent, a)
	if err != nil {
		return nil, err
	}
	sent := false
	defer func() { prepared.Finish(sent) }()
	child := root.memberSupervisor(prepared.Manifest)
	budget.publisher = child
	if err = child.listen(); err != nil {
		return nil, err
	}
	root.membersMu.Lock()
	if root.members == nil {
		root.members = make(map[string]*Supervisor)
	}
	root.members[child.id] = child
	root.membersMu.Unlock()
	// Start readers only after the trusted identity is in the member map.
	child.wg.Add(1)
	go child.acceptLoop()
	args := proto.CtlStartArgs{RunToken: child.runToken, Session: child.id, Agent: child.m.Agent.Kind, Argv: slices.Clone(child.m.Agent.Argv), Env: slices.Clone(child.m.Env), Cwd: parent.Cwd, Cols: child.m.Term.Cols, Rows: child.m.Term.Rows, Hooks: slices.Clone(child.m.Agent.Hooks)}
	shellStartArgs(child.m, &args)
	// P22 owns atomic member needs/value installation before the guest receives
	// sentinels. This call uses its one canonical egress process and engine.
	environment, err := root.installMemberPolicy(ctx, child.m, a.Secrets, a.SecretDelivery, a.SecretGeneration)
	if a.SecretDelivery != nil {
		for name, mode := range a.SecretDelivery.Modes {
			if mode == api.SecretModeBound {
				delete(a.Secrets, name)
			}
		}
	}
	if err != nil {
		child.closeSocket()
		root.removeMember(child.id)
		if preserveMember(err) {
			sent = true
			root.failMember()
			child.mu.Lock()
			child.closeRecord(host.EndBoot, "member policy setup was uncertain")
			snapshot := child.snapshot()
			child.mu.Unlock()
			_ = child.save(&snapshot, true)
		}
		return nil, err
	}
	sent = true // the one backend namespace transaction already committed
	args.Secrets, args.SecretModes, args.Missing = environment.Env, environment.Modes, environment.Missing
	if err := child.installPublicSecretView(a.SecretDelivery); err != nil {
		child.closeSocket()
		root.removeMember(child.id)
		root.failMember()
		child.mu.Lock()
		child.closeRecord(host.EndBoot, "member public state could not be confirmed")
		snapshot := child.snapshot()
		child.mu.Unlock()
		_ = child.save(&snapshot, true)
		return nil, &memberPolicyFailure{preserve: true, cause: err}
	}
	root.mu.Lock()
	hasCA := root.eg != nil && root.eg.caCert != ""
	proxy := guestProxyForDriver(root.m.Wall.Driver)
	root.mu.Unlock()
	if err := root.planMemberStores([]proto.CtlStartArgs{args}, hasCA, proxy); err != nil {
		child.closeSocket()
		root.removeMember(child.id)
		root.failMember()
		child.mu.Lock()
		child.closeRecord(host.EndBoot, "the member Store plan was refused")
		snapshot := child.snapshot()
		child.mu.Unlock()
		return nil, errors.Join(err, child.save(&snapshot, true))
	}
	if err := root.prepareMemberPhases(budget, &args, proto.CtlSpawn, true); err != nil {
		child.closeSocket()
		root.removeMember(child.id)
		child.mu.Lock()
		child.closeRecord(host.EndBoot, "the member preparation did not complete")
		snapshot := child.snapshot()
		child.mu.Unlock()
		return nil, errors.Join(err, child.save(&snapshot, true))
	}
	if err := child.publishActiveBudget(budget); err != nil {
		return nil, errors.Join(err, root.abortPreparedMember(args, proto.CtlSpawn))
	}
	var result proto.CtlStartResult
	child.registerLaunch()
	err = root.ctl.requestDetached(ctx, proto.CtlSpawn, args, &result)
	if err == nil && !root.guestABI(result.GuestABI) {
		err = execRefusal("the guest PTY ownership schema does not match this runtime")
	}
	if err != nil {
		if controlWasNotSent(err) {
			cleanupErr := root.abortPreparedMember(args, proto.CtlSpawn)
			if cleanupErr == nil {
				root.failMember()
				child.closeSocket()
				root.removeMember(child.id)
				child.mu.Lock()
				child.closeRecord(host.EndBoot, "the member launch was not sent")
				snapshot := child.snapshot()
				child.mu.Unlock()
				return nil, errors.Join(err, child.save(&snapshot, true))
			}
			err = errors.Join(err, cleanupErr)
		}
		// A lost final ACK cannot prove that no holder or agent started.
		// Keep this exact child and its control socket in the current cohort.
		root.failMember()
		child.mu.Lock()
		child.m.Status = host.Status{State: host.StateStarting, Source: "start", Reason: "the member launch outcome could not be confirmed", Since: now().UTC()}
		snapshot := child.snapshot()
		child.mu.Unlock()
		uncertain := &proto.Error{Code: "member_uncertain", Message: "the member launch outcome could not be confirmed", Hint: "Inspect or stop this member before starting another one. The sandbox's other members keep running."}
		return nil, errors.Join(uncertain, err, child.save(&snapshot, false))
	}
	if err := child.started(result, args.Missing); err != nil {
		return nil, err
	}
	root.mu.Lock()
	rootPublication := root.snapshot()
	root.mu.Unlock()
	child.mu.Lock()
	childPublication := child.snapshot()
	child.mu.Unlock()
	return host.MemberPublicationInfo(&childPublication, true, &rootPublication)
}

func (s *Supervisor) memberSupervisor(m *host.Manifest) *Supervisor {
	return &Supervisor{loadedName: m.Name, lifetime: s.lifetime, generation: s.generation, runToken: s.generation + "/" + m.ID, id: m.ID, m: m, log: s.log, audit: s.audit, sb: s.sb, link: s.link, ctl: s.ctl, peerCheck: s.peerCheck, al: s.al, stop: s.stop, quit: s.quit, sig: s.sig, primary: s, rec: record{phase: host.LifeBooting}, relayWake: make(chan struct{})}
}
func (s *Supervisor) removeMember(id string) {
	s.membersMu.Lock()
	delete(s.members, id)
	s.membersMu.Unlock()
}
func (s *Supervisor) memberName(kind string) string {
	root := s.owner()
	if _, known := agents.Find(kind); !known {
		kind = "custom"
	}
	suffix := "-" + kind
	if kind == "" {
		suffix = "-sh"
	}
	if kind == "shell" {
		suffix = "-sh"
	}
	root.mu.Lock()
	primaryName := root.m.Name
	root.mu.Unlock()
	used := map[string]bool{primaryName: true}
	for _, child := range root.childList() {
		child.mu.Lock()
		used[child.m.Name] = true
		child.mu.Unlock()
	}
	for n := 1; ; n++ {
		tail := suffix
		if n > 1 {
			tail = fmt.Sprintf("%s%d", suffix, n)
		}
		runes := []rune(primaryName)
		limit := api.SessionNameMax - len([]rune(tail))
		if len(runes) > limit {
			runes = runes[:limit]
		}
		candidate := string(runes) + tail
		if !used[candidate] {
			return candidate
		}
	}
}

func (s *Supervisor) memberEvent(p []byte) bool {
	if s.primary != nil {
		return false
	}
	var ev proto.CtlEvent
	if json.Unmarshal(p, &ev) != nil || ev.Session == "" || ev.Session == s.id {
		return false
	}
	if child := s.member(ev.Session); child != nil {
		child.event(p)
	}
	// Unknown IDs never reach a member or mutate the primary.
	return true
}
func (s *Supervisor) saveMembers() {
	for _, child := range s.childList() {
		child.mu.Lock()
		dirty := child.dirty
		child.dirty = false
		m := child.snapshot()
		child.mu.Unlock()
		if dirty {
			_ = child.save(&m, false)
		}
	}
}
func (s *Supervisor) finishMembers(kind, reason string) {
	for _, child := range s.childList() {
		child.closeSocket()
		child.mu.Lock()
		child.closeRecord(kind, reason)
		m := child.snapshot()
		child.mu.Unlock()
		_ = child.save(&m, true)
		child.wg.Wait()
	}
}

func (s *Supervisor) stopOnlyMember(id string) error {
	root := s.owner()
	child := root.member(id)
	if child == nil || child == root {
		return execRefusal("the primary cannot be removed as a child")
	}
	ctx, cancel := phaseclock.WithDeadline(root.lifetimeContext(), phaseclock.Current.Now().Add(killGrace+stopSlack))
	defer cancel()
	if err := root.ctl.request(ctx, proto.CtlStopMember, proto.CtlMemberArgs{Session: id}, nil); err != nil {
		return err
	}
	child.closeSocket()
	child.mu.Lock()
	child.closeRecord(host.EndKill, "member removed")
	m := child.snapshot()
	child.mu.Unlock()
	err := child.save(&m, true)
	if err == nil {
		root.removeMember(id)
	}
	return err
}

func (s *Supervisor) guestABI(got string) bool {
	if s.m.Wall != nil && s.m.Wall.Driver == driverPlain {
		return got == proto.GuestPlainTestV1
	}
	if s.m.Wall != nil && s.m.Wall.Driver == "chv" {
		return got == proto.GuestOwnedPTYV2
	}
	return got == proto.GuestPTY1000V1
}

func (s *Supervisor) validMemberEnd(p []byte) string {
	var event proto.CtlEvent
	if json.Unmarshal(p, &event) != nil || event.Event != proto.EventEnded || s.member(event.Session) == nil || !validEnd(p, event.Session) {
		return ""
	}
	return event.Session
}
