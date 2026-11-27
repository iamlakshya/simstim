//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// ForkSourceV1 retains an actual outside operation and the current native
// controller that accepted that SAME operation. It is never made from a row.
type ForkSourceV1 struct {
	mu             sync.Mutex
	operation      *SessionOperation
	warm           *WarmResumeV1
	selected       string
	memberRoot     *os.Root
	memberReceipt  memberRootReceiptV1
	children       []*forkChildV1
	copied, closed bool
	closeErr       error
	retirement     nativeDescriptorRetirementsV1
}

type forkChildV1 struct {
	source          *ForkSourceV1
	reservation     *sessionReservation
	operation       *SessionOperation
	manifest        Manifest
	started, closed bool
	closeErr        error
	retirement      nativeDescriptorRetirementsV1
}

func captureForkSourceV1(ctx context.Context, selected string) (_ *ForkSourceV1, returned error) {
	operation, err := BeginSessionOperation(ctx, selected, 5*time.Second)
	if err != nil {
		return nil, err
	}
	retained := false
	defer func() {
		if !retained {
			returned = errors.Join(returned, operation.CloseWithError())
		}
	}()
	warm, cold, err := InspectResumeForOperationV1(ctx, operation)
	if err != nil {
		return nil, err
	}
	if cold != nil {
		return nil, errors.Join(forkFailureV1("sandbox_stopped", "fork needs its original running source sandbox"), cold.Close())
	}
	if warm == nil {
		return nil, forkFailureV1("fork_source", "the original running fork source is unavailable")
	}
	if warm.owner == nil || !slices.Contains(warm.owner.features, api.FeatureFork) {
		return nil, errors.Join(UserError("helper_old", "the original running supervisor does not support fork", "Stop and resume the source with the current helper, then retry fork."), warm.Close())
	}
	result := &ForkSourceV1{operation: operation, warm: warm, selected: selected}
	if selected != operation.Primary() {
		if err := result.captureMemberV1(ctx); err != nil {
			return nil, errors.Join(err, warm.Close())
		}
	}
	retained = true
	return result, nil
}

func (source *ForkSourceV1) check(ctx context.Context) error {
	if source == nil || source.closed || source.closeErr != nil || source.operation == nil || source.warm == nil {
		return forkFailureV1("fork_source", "the original native fork source ended")
	}
	if err := source.warm.Check(ctx, source.operation); err != nil {
		return err
	}
	if source.selected != source.operation.Primary() {
		_, err := source.memberV1(ctx)
		return err
	}
	return nil
}

func (source *ForkSourceV1) captureMemberV1(ctx context.Context) (returned error) {
	var opened *os.Root
	err := withPlacement(ctx, false, func(control *placementControl) error {
		receipt, exists := control.MemberRoots[source.selected]
		if !exists || receipt.Primary != source.operation.Primary() {
			return forkFailureV1("fork_member", "the selected member has no original directory creation receipt")
		}
		if err := validateMemberRootReceiptsV1(map[string]memberRootReceiptV1{source.selected: receipt}); err != nil {
			return err
		}
		if err := receipt.PrimaryRef.matchesRoot(source.operation.native.root); err != nil {
			return err
		}
		root, err := cachefs.OpenPrivateRoot(paths.SessionDir(source.selected), false)
		if err != nil {
			return err
		}
		opened = root
		if err := receipt.Directory.matchesRoot(root); err != nil {
			return err
		}
		member, err := readMemberRootManifestV1(root, source.selected, receipt)
		if err != nil || member == nil {
			return errors.Join(forkFailureV1("fork_member", "the original selected member manifest is absent or changed"), err)
		}
		source.memberRoot, source.memberReceipt = root, receipt
		return nil
	}, false)
	if err != nil && opened != nil {
		return errors.Join(err, opened.Close())
	}
	return err
}

func (source *ForkSourceV1) memberV1(ctx context.Context) (*Manifest, error) {
	if source == nil || source.memberRoot == nil || source.operation == nil || source.memberReceipt.Primary != source.operation.Primary() {
		return nil, forkFailureV1("fork_member", "the original selected member root is absent")
	}
	if err := errors.Join(ctx.Err(), source.memberReceipt.Directory.matchesRoot(source.memberRoot), source.memberReceipt.PrimaryRef.matchesRoot(source.operation.native.root)); err != nil {
		return nil, err
	}
	member, err := readMemberRootManifestV1(source.memberRoot, source.selected, source.memberReceipt)
	if err != nil || member == nil {
		return nil, errors.Join(forkFailureV1("fork_member", "the original selected member manifest ended or changed"), err)
	}
	return member, nil
}

func (source *ForkSourceV1) reserve(ctx context.Context, count int, name string) error {
	if err := source.check(ctx); err != nil {
		return err
	}
	parent, err := source.operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	if parent.Wall == nil || parent.Files == nil || parent.Wall.Level == "none" {
		return forkFailureV1("fork_unwalled", "fork needs a sandbox with its own workspace")
	}
	if source.selected != parent.ID {
		member, err := source.memberV1(ctx)
		if err != nil {
			return err
		}
		template, err := forkSelectedTemplateV1(*parent, *member)
		if err != nil {
			return err
		}
		parent = &template
	}
	for index := 1; index <= count; index++ {
		reservation, err := reserveSessionID("")
		if err != nil {
			return err
		}
		operation, err := beginReservedSessionOperation(ctx, reservation)
		if err != nil {
			reservation.close()
			return err
		}
		child := &forkChildV1{source: source, reservation: reservation, operation: operation}
		source.children = append(source.children, child)
		requested := name
		if requested == "" {
			requested = forkDerivedNameV1(parent.Name, "-fork")
		}
		if count > 1 {
			requested = forkDerivedNameV1(requested, fmt.Sprintf("-%d", index))
		}
		child.manifest, err = forkChildManifestV1(*parent, reservation.id, requested)
		if err != nil {
			return err
		}
		child.manifest.ForkOf = source.selected
		child.manifest.Name, err = freeName(child.manifest.ID, child.manifest.Name, true)
		if err != nil {
			return err
		}
		child.manifest.SandboxName = child.manifest.Name
		applySessionIdentity(&child.manifest)
	}
	return source.check(ctx)
}

func forkDerivedNameV1(base, suffix string) string {
	if base == "" {
		base = "session"
	}
	runes := []rune(base)
	limit := api.SessionNameMax - len([]rune(suffix))
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes) + suffix
}

// This merges command DATA only. The caller must separately hold the
// original member receipt and require its actual native controller at copy.
func forkSelectedTemplateV1(primary, member Manifest) (Manifest, error) {
	if primary.SandboxOf != "" || member.ID == primary.ID || member.SandboxOf != primary.ID || member.Wall == nil {
		return Manifest{}, forkFailureV1("fork_member", "the selected command does not belong to this primary sandbox")
	}
	parent := CloneManifest(primary)
	parent.Agent = CloneManifest(member).Agent
	parent.Name, parent.Task, parent.Cwd = member.Name, member.Task, member.Cwd
	parent.Env = slices.Clone(member.Env)
	parent.Secrets = slices.Clone(member.Secrets)
	parent.SecretState = api.CloneSessionSecretState(member.SecretState)
	if member.Policy != nil {
		policy := api.ClonePolicy(*member.Policy)
		parent.Policy = &policy
		parent.Layers = api.CloneLayers(member.Layers)
	}
	return parent, nil
}

// forkChildManifestV1 creates DATA only. The original native source,
// reservation, copy borrow, admission and new native creator remain required.
func forkChildManifestV1(parent Manifest, id, name string) (Manifest, error) {
	if !validID(id) || id == parent.ID || parent.Wall == nil || parent.SandboxOf != "" {
		return Manifest{}, forkFailureV1("fork_source", "the child template has no distinct primary sandbox")
	}
	agent, known := agents.Find(parent.Agent.Kind)
	if !known {
		return Manifest{}, forkFailureV1("fork_agent", "the agent has no canonical fork or continue command")
	}
	command, err := agent.ForkCommand(parent.Agent.AgentSessionID)
	if err != nil {
		return Manifest{}, forkFailureV1("fork_agent", err.Error())
	}
	child := CloneManifest(parent)
	at := time.Now().UTC()
	child.ID, child.Name, child.ForkOf, child.ForkPending = id, name, parent.ID, true
	child.ForkBranchOf = parent.ID
	child.CreatedAt, child.UpdatedAt = at, at
	child.LastActivityAt, child.LastAttachedAt = time.Time{}, time.Time{}
	child.HostVersion, child.HolderBinary = version.Version, ""
	child.Run = Runtime{Socket: paths.Socket(id), BootID: BootID(), StartEnvelope: true}
	child.Status = Status{State: StateStarting, Since: at}
	child.Guest, child.Life, child.Auxiliary, child.ResumePreparation = nil, nil, nil, nil
	child.AutomaticRestore, child.PolicyNext = nil, nil
	child.VMCaptureNonce, child.SandboxOf = "", ""
	child.CopiesPending, child.Armed, child.SSHAgent = false, false, false
	child.Inputs, child.DebugUntil = nil, nil
	child.PlacedBy, child.PlaceReason = "", ""
	child.SetupHook, child.ResumeHook, child.RunHook, child.SetupHash = "", "", "", ""
	child.Git = nil
	child.Secrets = slices.Clone(parent.Secrets)
	child.MissingSecrets = slices.Clone(parent.Secrets)
	child.SecretState = initialSecretState(initialDelivery(&parent))
	child.Agent.Argv = command
	if len(parent.Agent.Argv) > 0 && len(command) > 0 && parent.Wall.Level == "process" {
		child.Agent.Argv[0] = parent.Agent.Argv[0]
	}
	child.Agent.Argv = append(child.Agent.Argv, slices.Clone(parent.Agent.Hooks)...)
	child.Agent.Argv = append(child.Agent.Argv, slices.Clone(parent.Agent.Inline)...)
	child.Agent.Command = nil
	child.Agent.AgentSessionID, child.Agent.IDSource = "", ""
	child.Agent.IDHistory = nil
	child.Agent.Hooks = slices.Clone(parent.Agent.Hooks)
	if child.Files != nil {
		oldPrefix := brand.Name + "/" + parent.ID + "/"
		newPrefix := brand.Name + "/" + id + "/"
		rename := func(seed *Seed) {
			if strings.HasPrefix(seed.Branch, oldPrefix) {
				seed.Branch = newPrefix + strings.TrimPrefix(seed.Branch, oldPrefix)
			}
		}
		rename(&child.Files.Seed)
		for index := range child.Files.Repositories {
			rename(&child.Files.Repositories[index])
		}
	}
	// The hook placeholder expands for the child's own environment/socket.
	child.Env = AgentEnv(child.Env, map[string]string{brand.Env("SESSION"): id, brand.Env("HOOK_SOCK"): paths.HookSocket(id)})
	child.path, child.extra = "", nil
	child.launchStarted = false
	return child, nil
}

func (source *ForkSourceV1) copy(ctx context.Context) (returned error) {
	if err := source.check(ctx); err != nil {
		return err
	}
	owner := source.warm.owner
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if source.copied || len(source.children) == 0 || !source.warm.loanSent {
		return forkFailureV1("fork_source", "this original fork copy was consumed or ended")
	}
	if err := owner.line.SetDeadline(time.Time{}); err != nil {
		return err
	}
	// Cancellation during descriptor staging uses the same framed native
	// cancel lane. Never abandon a partially decoded response on a deadline.
	stageCancelled := make(chan error, 1)
	stopStage := context.AfterFunc(ctx, func() { stageCancelled <- source.warm.writeWarmControlV1(5, proto.NativeWarmCancelV1) })
	var stageJoin sync.Once
	var stageCancelErr error
	stageFired := false
	finishStage := func() bool {
		stageJoin.Do(func() {
			if !stopStage() {
				stageFired = true
				stageCancelErr = <-stageCancelled
			}
		})
		return stageFired
	}
	defer finishStage()
	children := make([]string, 0, len(source.children))
	for _, child := range source.children {
		if err := ctx.Err(); err != nil {
			return err
		}
		if child.source != source || child.operation == nil || child.reservation == nil || !child.operation.Valid(child.reservation.id) {
			return forkFailureV1("fork_destination", "the original child reservation changed before its loan")
		}
		file, err := child.operation.native.duplicateLockedFile(ctx)
		if err != nil {
			return err
		}
		arguments := proto.NativeForkDestinationArgsV1{Version: 1, Scope: proto.NativeStopArgsV1{Version: 1, Primary: source.operation.Primary(), Nonce: owner.state.Nonce}, Child: child.reservation.id}
		raw, err := json.Marshal(arguments)
		if err == nil {
			err = owner.line.writeLoan(proto.Request{ID: proto.NativeForkDestinationRequestV1, Op: proto.NativeForkDestinationV1, Args: raw}, file)
		}
		err = errors.Join(err, file.Close())
		if err != nil {
			return err
		}
		if err := source.warm.readNativeStateV1(proto.NativeForkDestinationRequestV1); err != nil {
			return err
		}
		children = append(children, child.reservation.id)
	}
	if finishStage() {
		return errors.Join(ctx.Err(), stageCancelErr)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	arguments := proto.NativeForkCopyArgsV1{Version: 1, Scope: proto.NativeStopArgsV1{Version: 1, Primary: source.operation.Primary(), Nonce: owner.state.Nonce}, SourceSession: source.selected, Children: children}
	raw, err := json.Marshal(arguments)
	if err != nil {
		return err
	}
	if err := owner.line.WriteFrame(proto.TRequest, proto.Request{ID: proto.NativeForkCopyRequestV1, Op: proto.NativeForkCopyV1, Args: raw}); err != nil {
		return err
	}
	cancelWritten := make(chan error, 1)
	stop := context.AfterFunc(ctx, func() { cancelWritten <- source.warm.writeWarmControlV1(5, proto.NativeWarmCancelV1) })
	var response *proto.Response
	joined := false
	for {
		current, err := source.warm.readNativeReplyV1()
		if err != nil {
			if !stop() {
				<-cancelWritten
			}
			return err
		}
		switch current.ID {
		case proto.NativeForkCopyRequestV1:
			copy := current
			response = &copy
		case 5:
			var state proto.NativeStopStateV1
			if !current.OK || !api.ExactJSONFields(current.Result, state) || json.Unmarshal(current.Result, &state) != nil || state != owner.state {
				if !stop() {
					<-cancelWritten
				}
				return forkFailureV1("fork_source", "the original native copy did not confirm cancellation and join")
			}
			joined = true
		default:
			if !stop() {
				<-cancelWritten
			}
			return forkFailureV1("fork_source", "the original native copy returned an unrelated reply")
		}
		if response == nil {
			continue
		}
		if stop() {
			if response.Error != nil {
				return response.Error
			}
			if !response.OK {
				return forkFailureV1("fork_copy", "the original source refused the fork copy")
			}
			var copied proto.NativeForkCopyResultV1
			if !api.ExactJSONFields(response.Result, copied) || json.Unmarshal(response.Result, &copied) != nil || copied.State != owner.state {
				return forkFailureV1("fork_source", "the frozen source copy returned another original scope")
			}
			for _, child := range source.children {
				agent, known := agents.Find(child.manifest.Agent.Kind)
				if !known || copied.AgentKind != child.manifest.Agent.Kind || copied.AgentSessionID != "" && !agent.ValidID(copied.AgentSessionID) {
					return forkFailureV1("fork_agent", "the frozen source copy returned an invalid conversation identity")
				}
				command, err := agent.ForkCommand(copied.AgentSessionID)
				if err != nil {
					return forkFailureV1("fork_agent", err.Error())
				}
				if child.manifest.Wall.Level == "process" && len(child.manifest.Agent.Argv) > 0 {
					command[0] = child.manifest.Agent.Argv[0]
				}
				command = append(command, slices.Clone(child.manifest.Agent.Hooks)...)
				child.manifest.Agent.Argv = append(command, slices.Clone(child.manifest.Agent.Inline)...)
			}
			source.copied = true
			return nil
		}
		if !joined {
			continue
		}
		if response.Error != nil {
			return errors.Join(ctx.Err(), response.Error, <-cancelWritten)
		}
		return errors.Join(ctx.Err(), <-cancelWritten)
	}
}

func (source *ForkSourceV1) Close() error {
	if source == nil {
		return nil
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.closed {
		return source.closeErr
	}
	// Join the SAME original copy/borrowers before closing any reservation.
	if source.warm != nil {
		if err := source.warm.Close(); err != nil {
			source.closeErr = rememberForkCloseV1(source.closeErr, err)
			return source.closeErr
		}
	}
	pending := false
	if source.memberRoot != nil {
		source.closeErr = rememberForkCloseV1(source.closeErr, source.retirement.closeRootsV4([]*os.Root{source.memberRoot}))
		if source.retirement.checkRootsV4([]*os.Root{source.memberRoot}) != nil {
			pending = true
		}
	}
	for _, child := range source.children {
		source.closeErr = rememberForkCloseV1(source.closeErr, child.closeResourcesV1())
		if child == nil || !child.closed {
			pending = true
		}
	}
	if pending {
		return source.closeErr
	}
	// An unjoined child must retain this original parent OP. No second Close
	// may skip that child because a metadata closed flag was set on error.
	if source.operation != nil {
		if err := source.operation.CloseWithError(); err != nil {
			source.closeErr = rememberForkCloseV1(source.closeErr, err)
			return source.closeErr
		}
	}
	source.closed = true
	return source.closeErr
}
