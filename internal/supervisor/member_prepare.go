package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/memberenv"
	"github.com/iamlakshya/simstim/internal/phaseclock"
	"github.com/iamlakshya/simstim/internal/proto"
)

const memberStoreLimit = 30 * time.Second

type trustedStoreBatch struct {
	command      []string
	cwd          string
	env          []string
	sentinel     api.Secret
	allocated    bool
	acknowledged bool
}

func (b *trustedStoreBatch) same(other *trustedStoreBatch) bool {
	return b.cwd == other.cwd && b.sentinel == other.sentinel && slices.Equal(b.command, other.command) && slices.Equal(b.env, other.env)
}
func trustedMemberStore(a proto.CtlStartArgs, hasCA bool, proxy string) (*trustedStoreBatch, error) {
	if a.SecretModes == nil {
		return nil, nil
	}
	bound := map[string]api.Secret{}
	if len(a.SecretModes) != len(a.Secrets) {
		return nil, secretIntentFailure()
	}
	for name, value := range a.Secrets {
		mode, exists := a.SecretModes[name]
		if !exists || !mode.Valid() {
			return nil, secretIntentFailure()
		}
		if mode == api.SecretModeBound {
			bound[name] = api.Secret(value)
		}
	}
	env := slices.Clone(a.Env)
	var err error
	if hasCA {
		env, err = memberenv.Trust(env, a.Agent, filepath.Join(host.GuestRunDir(), "ca.pem"), filepath.Join(host.GuestRunDir(), "ca-bundle.pem"))
		if err != nil {
			return nil, err
		}
	}
	// The runtime URI is one sandbox-owned rewrite. A canonical identity here
	// preserves equality without trusting an ephemeral guest-selected port.
	env = memberenv.Proxy(env, host.DefaultGuestProxy, proxy)
	plans, env, err := memberenv.PrepareStores(a.Session, a.Agent, a.Cwd, env, bound)
	if err != nil || len(plans) == 0 {
		return nil, err
	}
	if len(plans) != 1 {
		return nil, execRefusal("the selected members need conflicting Store targets")
	}
	plan := plans[0]
	return &trustedStoreBatch{command: slices.Clone(plan.Argv), cwd: a.Cwd, env: env, sentinel: plan.Sentinel}, nil
}
func (s *Supervisor) planMemberStores(args []proto.CtlStartArgs, hasCA bool, proxy string) error {
	root := s.owner()
	selected := make(map[string]bool, len(args))
	batch := root.storeBatch
	for _, a := range args {
		if needsMemberPreparation(a) {
			if err := proto.CheckMemberPrepareFrames(memberTarget(root, a.Session), a); err != nil {
				return err
			}
		}
		planned, err := trustedMemberStore(a, hasCA, proxy)
		if err != nil {
			return err
		}
		if planned == nil {
			continue
		}
		selected[a.Session] = true
		if batch == nil {
			batch = planned
		} else if !batch.same(planned) {
			return execRefusal("the selected members need different stored-login contexts; start a fresh sandbox")
		}
	}
	root.storeBatch, root.memberStores = batch, selected
	return nil
}
func memberTarget(s *Supervisor, id string) string {
	if id == s.id {
		return proto.CtlStart
	}
	return proto.CtlSpawn
}
func (s *Supervisor) requirePreparedGuest(ctx context.Context, detached bool) error {
	var raw json.RawMessage
	request := s.ctl.request
	if detached {
		request = s.ctl.requestDetached
	}
	if err := request(ctx, proto.CtlGuestCapabilities, struct{}{}, &raw); err != nil {
		return err
	}
	var result proto.CtlGuestFeatures
	if !api.ExactJSONFields(raw, result) || json.Unmarshal(raw, &result) != nil || result.Version != 1 || !slices.Contains(result.Features, proto.GuestMemberPrepareV1) {
		return execRefusal("the actual guest cannot prepare bounded member phases")
	}
	return nil
}
func (s *Supervisor) prepareMemberPhases(b *activeBootBudget, args *proto.CtlStartArgs, target string, live bool) (returned error) {
	if err := s.requireConfirmedStoreBatch(); err != nil {
		return err
	}
	// Genuine legacy nil-mode/no-input starts keep their original direct op.
	if !needsMemberPreparation(*args) {
		return nil
	}
	if b == nil || s.ctl == nil {
		return execRefusal("the member has no owned control stream or remaining startup budget")
	}
	if err := s.publishActiveBudget(b); err != nil {
		return err
	}
	request := s.ctl.request
	if live {
		request = s.ctl.requestDetached
	}
	if err := s.requirePreparedGuest(b, live); err != nil {
		return err
	}
	if err := proto.CheckMemberPrepareFrames(target, *args); err != nil {
		return err
	}
	var raw json.RawMessage
	preparation := proto.CtlMemberPrepareArgs{Version: 1, Target: target, Start: *args}
	defer func() {
		if returned != nil {
			returned = errors.Join(returned, s.abortPreparedMember(*args, target))
		}
	}()
	if err := request(b, proto.CtlMemberPrepare, preparation, &raw); err != nil {
		return err
	}
	var result proto.CtlMemberPrepareResult
	if !api.ExactJSONFields(raw, result) || json.Unmarshal(raw, &result) != nil || result.Version != 1 || !api.InputNonce(result.Claim).Valid() {
		return execRefusal("the member preparation claim is invalid")
	}
	args.PreparedClaim = result.Claim
	if !s.memberStores[args.Session] {
		return nil
	}
	store := func(ctx context.Context) error {
		var raw json.RawMessage
		err := request(ctx, proto.CtlMemberStore, proto.CtlMemberPrepareArgs{Version: 1, Target: target, Start: *args}, &raw)
		if err == nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		if err == nil {
			fields, decodeErr := api.JSONObjectFields(raw)
			if decodeErr != nil || len(fields) != 0 {
				err = execRefusal("the member Store acknowledgement is invalid")
			}
		}
		if err != nil {
			s.failMember()
			return err
		}
		s.storeBatch.acknowledged = true
		return nil
	}
	if s.storeBatch == nil {
		return secretIntentFailure()
	}
	if s.storeBatch.allocated {
		return store(b)
	} // the required dedup call charges active time
	s.storeBatch.allocated = true // occupied even when the ACK is lost or Store fails
	return s.separatePhase(b, phaseclock.Current.Now().Add(memberStoreLimit), func(ctx context.Context) error {
		// A guest timeout may be joining its owned command at this boundary. An
		// absent ACK is uncertainty for this member, never permission to kill a
		// running sibling or rerun the occupied Store transaction.
		// Unique Store timeout isolation is required even during initial boot.
		request = s.ctl.requestDetached
		return store(ctx)
	})
}

func needsMemberPreparation(args proto.CtlStartArgs) bool {
	return args.SecretModes != nil || args.InputNonce != "" || args.InputDigest != "" || args.PreparedClaim != ""
}

func (s *Supervisor) requireConfirmedStoreBatch() error {
	root := s.owner()
	batch := root.storeBatch
	if batch != nil && batch.allocated && !batch.acknowledged {
		root.failMember()
		return &proto.Error{Code: "secret_store", Message: "the stored login operation was not confirmed in this sandbox", Hint: "Stop the sandbox and start it afresh. An unconfirmed Store cannot be retried or bypassed by another member."}
	}
	return nil
}
