package client

import (
	"context"
	"fmt"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

type preparedPlacementBindingV1 struct {
	attempt   *HostAttemptV1
	plan      *newSecretPlan
	record    *ownedPlacementRecord
	member    string
	cap       int64
	required  int64
	predicted int64
}

func preparedPlacementCapV1(wall string, memory int64, layers []api.Layer, kind string) (int64, error) {
	if wall != "gvisor" && wall != "microvm" {
		return 0, grantOperationError()
	}
	if memory > 0 {
		return memory, checkPlacementCapRepresentableV1(memory)
	}
	if wall == "microvm" {
		return 0, vmPrepareUnavailable()
	}
	policy, _, err := egress.MergeLayers(layers)
	if err != nil {
		return 0, policyError(err)
	}
	var defaults *api.SandboxSize
	if agent, known := agents.Find(kind); known {
		defaults = agent.SandboxSize()
	}
	size, err := api.ResolveSandboxSize(policy.Sandbox, nil, defaults)
	if err != nil {
		return 0, err
	}
	return size.Memory, nil
}

func bindPreparedPlacementMemberV1(ctx context.Context, attempt *HostAttemptV1) error {
	if attempt == nil || attempt.plan == nil || attempt.plan.automatic == nil || attempt.request == nil || attempt.conn == nil || attempt.placement == nil {
		return grantOperationError()
	}
	attempt.mu.Lock()
	before := attempt.placementBinding
	attempt.mu.Unlock()
	r := attempt.request
	r.mu.Lock()
	secret, vm := r.newSecrets, r.preparedVM
	valid := r.hostAttempt == attempt && r.hostSelection == attempt.plan.selection && r.selectedHost == attempt.host.Name
	r.mu.Unlock()
	if secret == nil || !valid {
		return grantOperationError()
	}
	secret.mu.Lock()
	valid = secret.conn == attempt.conn && !secret.closed && !secret.busy && (!secret.attempted || before != nil && before.plan == secret) && !secret.published && !secret.retired && !secret.aborting && secret.creation != nil && secret.selection != nil
	if !valid {
		secret.mu.Unlock()
		return grantOperationError()
	}
	member, hostID, wall, memory, kind := secret.creation.SessionID, secret.creation.HostID, secret.header.Wall, secret.header.MemoryMax, secret.selection.Kind()
	layers, operation, preparedVM := api.CloneLayers(secret.layers), secret.operation, secret.vm
	secret.mu.Unlock()
	if !api.ValidSessionID(member) || hostID != attempt.conn.HostID() || operation == nil {
		return grantOperationError()
	}
	if err := operation.Revalidate(ctx); err != nil {
		return err
	}
	cap, err := preparedPlacementCapV1(wall, memory, layers, kind)
	if err != nil {
		return err
	}
	if wall == "microvm" {
		if preparedVM == nil || preparedVM != vm {
			return vmPrepareUnavailable()
		}
		if _, err := preparedVM.EncodedFor(attempt.conn); err != nil {
			return err
		}
		budget, err := vmneed.BudgetFor(preparedVM.Need().GuestRAM)
		if err != nil || budget.Limit != cap {
			return vmPrepareUnavailable()
		}
	}
	if attempt.candidate == nil || wall != attempt.candidate.level {
		return placementPreparedMismatchV1("the actual wall differs from the original placement choice")
	}
	if err := checkPlacementCapRepresentableV1(cap); err != nil {
		return err
	}
	binding := &preparedPlacementBindingV1{attempt: attempt, plan: secret, record: attempt.placement, member: member, cap: cap, required: cap, predicted: attempt.candidate.declared}
	if before != nil && (before.attempt != attempt || before.plan != secret || before.record != binding.record || before.member != member || before.cap != cap || before.required != binding.required || before.predicted != binding.predicted) {
		return grantOperationError()
	}
	if err := attempt.placement.bindPreparedMemberV1(ctx, binding); err != nil {
		return err
	}
	attempt.mu.Lock()
	if attempt.closing || attempt.started || attempt.placement != binding.record {
		attempt.mu.Unlock()
		return grantOperationError()
	}
	attempt.placementBinding = binding
	attempt.mu.Unlock()
	return nil
}

func (record *ownedPlacementRecord) bindPreparedMemberV1(ctx context.Context, binding *preparedPlacementBindingV1) error {
	if record == nil || record.store == nil || binding == nil || binding.record != record || binding.attempt == nil || binding.required != binding.cap {
		return grantOperationError()
	}
	return record.store.locked(ctx, "placements.json", func(owned context.Context) error {
		state, err := record.store.readPlacements(owned)
		if err != nil {
			return err
		}
		found := -1
		for i, current := range state.Records {
			if current.ID == record.record.ID {
				found = i
				continue
			}
			if current.Host == record.record.Host && current.HostID == record.record.HostID && current.Generation == record.record.Generation && current.Session == binding.member && current.Pending {
				return placementStoreError(errPlacementState)
			}
		}
		if found < 0 || !samePlacementRecordV1(state.Records[found], record.record) || record.record.Session != "" && (record.record.Session != binding.member || !record.record.Pending) {
			return placementStoreError(errPlacementState)
		}
		if record.record.Session == binding.member && record.record.Pending {
			return nil
		}
		if binding.cap > record.record.Memory {
			candidate := binding.attempt.candidate
			if candidate == nil || candidate.observation == nil {
				return grantOperationError()
			}
			others := make([]placementRecordV1, 0, len(state.Records)-1)
			for i, value := range state.Records {
				if i != found {
					others = append(others, value)
				}
			}
			residual, err := placementInFlight(others, candidate.observation, time.Now(), candidate.placementFence)
			if err != nil {
				return err
			}
			room := candidate.observation.facts.Room
			if binding.cap > room || residual > room-binding.cap {
				return placementPreparedMismatchV1("the actual prepared cap no longer fits the local room reservation")
			}
		}
		next := record.record
		next.Memory = binding.cap
		next.Session, next.Pending = binding.member, true
		state.Records[found] = next
		if err := record.store.write(owned, "placements.json", state); err != nil {
			return err
		}
		record.record = next
		return nil
	})
}

// The actual first writer rechecks this SAME prepared plan and local member
// binding. Header IDs, caller fields and an unrelated record cannot donate it.
func checkPreparedPlacementWireV1(ctx context.Context, attempt *HostAttemptV1, actual NewArgs) error {
	if attempt.plan.automatic == nil {
		return nil
	}
	attempt.mu.Lock()
	binding := attempt.placementBinding
	attempt.mu.Unlock()
	if binding == nil || binding.attempt != attempt || binding.record != attempt.placement || actual.ID != binding.member {
		return grantOperationError()
	}
	r := attempt.request
	r.mu.Lock()
	same := r.newSecrets == binding.plan
	r.mu.Unlock()
	if !same {
		return grantOperationError()
	}
	plan := binding.plan
	plan.mu.Lock()
	valid := plan.conn == attempt.conn && !plan.closed && !plan.aborting && !plan.retired && !plan.published && plan.busy && plan.creation != nil && plan.creation.SessionID == binding.member && plan.creation.HostID == attempt.conn.HostID() && plan.header.Wall == actual.Wall && plan.selection != nil
	kind := ""
	if plan.selection != nil {
		kind = plan.selection.Kind()
	}
	plan.mu.Unlock()
	if !valid {
		return grantOperationError()
	}
	cap, err := preparedPlacementCapV1(actual.Wall, actual.MemoryMax, actual.Layers, kind)
	if err != nil || cap != binding.cap {
		return placementPreparedMismatchV1("the physical New has another complete memory cap")
	}
	current := binding.record.record
	if current.Session != binding.member || !current.Pending || current.Memory != binding.required {
		return grantOperationError()
	}
	return binding.record.store.locked(ctx, "placements.json", func(owned context.Context) error {
		state, err := binding.record.store.readPlacements(owned)
		if err != nil {
			return err
		}
		for _, saved := range state.Records {
			if saved.ID == current.ID && samePlacementRecordV1(saved, current) {
				return nil
			}
		}
		return placementStoreError(errPlacementState)
	})
}

func placementPreparedMismatchV1(message string) error {
	return &UserError{Msg: fmt.Sprintf("automatic placement changed before the original start: %s", message), Hint: "Start a fresh request with an explicit matching wall and sandbox size.", Code: "placement_changed", Kind: KindRefused}
}

// Every supported Linux memory page is a multiple of the base 4 KiB page.
// This rejects only definitely unrepresentable caps. The actual native host
// retains its stronger exact page/controller check before guest execution.
func checkPlacementCapRepresentableV1(cap int64) error {
	if cap < api.MinMemoryMax || cap%4096 != 0 {
		return placementPreparedMismatchV1("the prepared cap is below the supported minimum or is not a whole memory page")
	}
	return nil
}
