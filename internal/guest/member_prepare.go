package guest

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

type preparedMemberState uint8

const (
	memberPreparing preparedMemberState = iota
	memberStorePending
	memberStoreRunning
	memberReady
	memberConsumed
	memberFailed
)

// One record retains both the unmodified request and the actual launch/Store
// projection. Failed and consumed records remain occupied for this Init's run.
type preparedMember struct {
	claim         string
	target        string
	raw           proto.CtlStartArgs
	projected     proto.CtlStartArgs
	state         preparedMemberState
	storeRequired bool
}

func cloneMemberArgs(a proto.CtlStartArgs) proto.CtlStartArgs {
	a.Decoys = proto.CloneDecoyFilesV1(a.Decoys)
	a.PackageManagers = proto.ClonePackageManagersV1(a.PackageManagers)
	a.Argv, a.Env = slices.Clone(a.Argv), slices.Clone(a.Env)
	a.Hooks, a.Missing, a.StateFiles = slices.Clone(a.Hooks), slices.Clone(a.Missing), slices.Clone(a.StateFiles)
	a.Secrets, a.SecretModes = maps.Clone(a.Secrets), maps.Clone(a.SecretModes)
	return a
}

func sameMemberIntent(a, b proto.CtlStartArgs) bool {
	if (a.SecretModes == nil) != (b.SecretModes == nil) {
		return false
	}
	a.PreparedClaim, b.PreparedClaim = "", ""
	// Empty slices carry the same intent. Explicit mode-map presence does not.
	if len(a.Argv) == 0 {
		a.Argv = nil
	}
	if len(b.Argv) == 0 {
		b.Argv = nil
	}
	if len(a.Env) == 0 {
		a.Env = nil
	}
	if len(b.Env) == 0 {
		b.Env = nil
	}
	left, e1 := json.Marshal(a)
	right, e2 := json.Marshal(b)
	return e1 == nil && e2 == nil && bytes.Equal(left, right)
}

func (g *Init) decodeMemberPreparation(raw []byte) (proto.CtlMemberPrepareArgs, error) {
	var args proto.CtlMemberPrepareArgs
	fields, err := api.JSONObjectFields(raw, "version", "target", "start")
	if err != nil || !api.ExactJSONFields(raw, proto.CtlMemberPrepareArgs{}) || len(fields) != 3 || json.Unmarshal(raw, &args) != nil || args.Version != 1 || (args.Target != proto.CtlStart && args.Target != proto.CtlSpawn) {
		return args, execError("invalid member preparation")
	}
	if !api.ExactJSONFields(fields["start"], proto.CtlStartArgs{}) {
		return args, execError("invalid member preparation fields")
	}
	args.Start, err = g.decodeMember(fields["start"])
	if err != nil {
		return args, err
	}
	if !api.ValidSessionID(args.Start.Session) || args.Start.RunToken == "" {
		return args, execError("member preparation has no current run identity")
	}
	return args, nil
}

func (g *Init) prepareMember(raw []byte) (proto.CtlMemberPrepareResult, error) {
	args, err := g.decodeMemberPreparation(raw)
	if err != nil {
		return proto.CtlMemberPrepareResult{}, err
	}
	a := args.Start
	fields, _ := api.JSONObjectFields(raw, "version", "target", "start")
	startFields, _ := api.JSONObjectFields(fields["start"])
	if _, present := startFields["prepared_claim"]; present {
		return proto.CtlMemberPrepareResult{}, execError("member preparation cannot reuse a claim")
	}
	if err = proto.CheckMemberPrepareFrames(args.Target, a); err != nil {
		return proto.CtlMemberPrepareResult{}, err
	}
	if _, err = memberBoundSentinels(a); err != nil {
		return proto.CtlMemberPrepareResult{}, err
	}
	if g.vm != nil {
		if err = g.vm.validateMember(a, args.Target == proto.CtlStart); err != nil {
			return proto.CtlMemberPrepareResult{}, err
		}
	}
	var nonce [32]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return proto.CtlMemberPrepareResult{}, err
	}
	pending := &preparedMember{claim: hex.EncodeToString(nonce[:]), target: args.Target, raw: cloneMemberArgs(a), state: memberPreparing}
	g.mu.Lock()
	busy := g.lifetime == nil || g.lifetime.Err() != nil || g.ended || g.starting || g.members[a.Session] != nil || g.memberStarts[a.Session] || g.preparedMembers[a.Session] != nil
	if args.Target == proto.CtlStart {
		busy = busy || g.holder != nil || !g.inputStartAllowedLocked(a)
	} else {
		busy = busy || !g.sessionPrepared || a.InputNonce != "" || a.InputDigest != ""
	}
	if busy {
		g.mu.Unlock()
		return proto.CtlMemberPrepareResult{}, execError("the member cannot reserve this preparation")
	}
	if g.preparedMembers == nil {
		g.preparedMembers = make(map[string]*preparedMember)
	}
	if g.memberStarts == nil {
		g.memberStarts = make(map[string]bool)
	}
	g.preparedMembers[a.Session] = pending
	g.memberStarts[a.Session] = true
	g.commandHolds++
	if args.Target == proto.CtlStart {
		g.starting = true
		g.primary = a.Session
		g.pendingBoot = a.PendingMembers
		g.bootAdmission = true
	}
	g.mu.Unlock()
	err = g.projectMember(&a, args.Target == proto.CtlStart)
	var required bool
	if err == nil {
		var bound map[string]api.Secret
		bound, err = memberBoundSentinels(a)
		if err == nil && a.SecretModes != nil {
			var plans []secretStorePlan
			plans, _, err = prepareSecretStores(a.Session, a.Agent, a.Cwd, a.Env, bound)
			required = len(plans) > 0
			if len(plans) > 1 {
				err = secretStoreConflict(plans[0].name)
			}
		}
	}
	g.mu.Lock()
	g.commandHolds--
	if args.Target == proto.CtlStart {
		g.starting = false
	}
	if err != nil || g.ended {
		pending.state = memberFailed
		delete(g.memberStarts, a.Session)
		if err == nil {
			err = execError("the guest ended during member preparation")
		}
	} else {
		pending.projected = cloneMemberArgs(a)
		pending.storeRequired = required
		pending.state = memberReady
		if required {
			pending.state = memberStorePending
		}
	}
	g.mu.Unlock()
	g.endIfEmpty()
	if err != nil {
		return proto.CtlMemberPrepareResult{}, err
	}
	return proto.CtlMemberPrepareResult{Version: 1, Claim: pending.claim}, nil
}

// projectMember does infrastructure/trust work only. Both primary and children
// receive their own agent's CA variables and the same sandbox proxy projection.
func (g *Init) projectMember(a *proto.CtlStartArgs, primary bool) error {
	if g.unwalled != nil {
		return g.projectUnwalledV1(a, primary)
	}
	if err := g.resolveShell(a); err != nil {
		return err
	}
	if primary {
		prepared, err := g.useInputPreparation(a)
		if err != nil {
			return err
		}
		if !prepared {
			if err = g.prepareSession(a); err != nil {
				return err
			}
		}
	}
	if err := g.checkBootDecoys(a.Decoys); err != nil {
		return err
	}
	if err := g.checkBootPackageSettings(a.PackageManagers); err != nil {
		return err
	}
	packageEnv, err := PackageManagerEnvironment(a.PackageManagers, a.Env)
	if err != nil {
		return err
	}
	a.Env = packageEnv
	env, err := g.childTrustEnv(a.Env, a.Agent)
	if err != nil {
		return err
	}
	a.Env = env
	if err := g.memberProxyEnv(a, false); err != nil {
		return err
	}
	return g.memberSSHAgentEnv(a)
}

func (g *Init) preparedMatchesLocked(a proto.CtlStartArgs, target string, state preparedMemberState) bool {
	p := g.preparedMembers[a.Session]
	return p != nil && g.lifetime != nil && g.lifetime.Err() == nil && !g.ended && p.claim == a.PreparedClaim && p.target == target && p.state == state && sameMemberIntent(p.raw, a) && g.memberStarts[a.Session]
}

func (g *Init) storePreparedMember(raw []byte) error {
	args, err := g.decodeMemberPreparation(raw)
	if err != nil {
		return err
	}
	g.mu.Lock()
	if !g.preparedMatchesLocked(args.Start, args.Target, memberStorePending) {
		g.mu.Unlock()
		return execError("no matching pending member Store claim")
	}
	p := g.preparedMembers[args.Start.Session]
	p.state = memberStoreRunning
	g.commandHolds++
	a := cloneMemberArgs(p.projected)
	parent := g.lifetime
	g.mu.Unlock()
	// New owns the real lifetime. Literal fixtures must supply one to execute.
	if parent == nil {
		err = execError("the member has no guest lifetime")
	} else {
		err = g.prepareMemberSecretStore(parent, a)
	}
	g.mu.Lock()
	g.commandHolds--
	if err != nil || g.ended {
		p.state = memberFailed
		delete(g.memberStarts, a.Session)
		if err == nil {
			err = execError("the guest ended during member Store")
		}
	} else {
		p.state = memberReady
	}
	g.mu.Unlock()
	g.endIfEmpty()
	return err
}

func (g *Init) consumePreparedMember(a proto.CtlStartArgs, primary bool) (proto.CtlStartArgs, error) {
	target := proto.CtlSpawn
	if primary {
		target = proto.CtlStart
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.preparedMatchesLocked(a, target, memberReady) {
		return a, execError("the member preparation is incomplete, changed or consumed")
	}
	p := g.preparedMembers[a.Session]
	p.state = memberConsumed
	return cloneMemberArgs(p.projected), nil
}

func (preparedMember) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, api.Redacted) }
