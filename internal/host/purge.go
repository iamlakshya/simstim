package host

import (
	"context"
	"errors"
	"os"
	"os/user"
	"runtime"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// purgeOwnerV1 is a live original helper/control owner. Saved paths, drain
// metadata, a report and a nonce cannot construct a session or native stop.
type purgeOwnerV1 struct {
	ctx       context.Context
	placement *placement
	args      api.PurgeArgs
	run       string
	process   placementOperationRecord
	report    api.PurgeResult
	roots     map[string]*purgeRootPinV1
}

// Purge removes only resources for which the actual producers supplied
// ownership. Force bypasses working/unsaved-work questions, never native stop,
// root identity, unknown data, replacement or ownership checks.
func Purge(args api.PurgeArgs) (api.PurgeResult, error) {
	return PurgeContext(context.Background(), args)
}

func PurgeContext(ctx context.Context, args api.PurgeArgs) (result api.PurgeResult, returned error) {
	if ctx == nil {
		return result, UserError("purge", "the removal has no original request lifetime", "Keep the host saved and retry the same removal.")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	owner, err := beginPurgeOwnerV1(ctx, args)
	if err != nil {
		return result, err
	}
	defer func() {
		for _, pin := range owner.roots {
			returned = errors.Join(returned, pin.root.Close())
		}
		returned = errors.Join(returned, owner.release())
		result = owner.report
		if returned != nil {
			var wire *proto.Error
			if !errors.As(returned, &wire) {
				returned = UserError("purge", "host removal stopped: "+returned.Error(), "Keep the host saved and run the same host rm --purge command again.")
			}
		}
	}()
	// Drain already excludes new admissions. Settle previously admitted work
	// before pinning roots or enumerating ANY work-loss inspection subjects.
	// This wait grants no native stopped or cleanup authority.
	if err := waitPlacementOperations(ctx, owner.placement); err != nil {
		return owner.report, err
	}
	if err := owner.pinRoots(); err != nil {
		return owner.report, err
	}
	if err := owner.precheck(); err != nil {
		return owner.report, err
	}
	if err := owner.removeSessions(); err != nil {
		return owner.report, err
	}
	if err := owner.undoFootprints(); err != nil {
		return owner.report, err
	}
	if err := owner.removeOwnedRoots(); err != nil {
		return owner.report, err
	}
	if err := owner.removeOwnedHelpers(); err != nil {
		return owner.report, err
	}
	if err := owner.prepareFinalGuard(); err != nil {
		return owner.report, err
	}
	return owner.report, nil
}

func beginPurgeOwnerV1(ctx context.Context, args api.PurgeArgs) (*purgeOwnerV1, error) {
	if !ValidHostID(args.HostID) || !ValidHostID(args.Nonce) {
		return nil, UserError("bad_request", "removal needs the original host identity and removal nonce", "Retry through the same saved host's removal record.")
	}
	if err := errors.Join(CheckCaller(), ctx.Err()); err != nil {
		return nil, err
	}
	p, err := factsPlacementOwnerV1(ctx)
	if err != nil {
		return nil, err
	}
	if p.hostID != args.HostID {
		return nil, placementError("the purge request names another original host")
	}
	run, err := footprintNonce()
	if err != nil {
		return nil, err
	}
	birth, err := placementProcessBirth(os.Getpid())
	if err != nil {
		return nil, err
	}
	account, err := user.Current()
	if err != nil {
		return nil, err
	}
	owner := &purgeOwnerV1{ctx: ctx, placement: p, args: args, run: run, process: placementOperationRecord{PID: os.Getpid(), Birth: birth, Kind: "purge", At: time.Now().UTC()}}
	err = withPlacementRoot(ctx, true, func(control *placementControl, _ *os.Root) error {
		if control.LockdownRemoval != nil {
			return UserError("busy", "an unfinished native lockdown removal still owns its captured state", "Finish the original lockdown change before retrying this host removal.")
		}
		if control.Drain != "" && (control.Drain != "purge" || control.Nonce != args.Nonce) {
			return UserError("busy", "another removal owns this host generation", "Keep the host saved and resume the original removal.")
		}
		if control.Owner != nil {
			current, readErr := placementProcessBirth(control.Owner.PID)
			if readErr == nil && current == control.Owner.Birth {
				return UserError("busy", "the original removal process is still running", "Wait for that exact removal to finish before retrying.")
			}
			if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				return placementError("the previous removal process lifetime is uncertain")
			}
		}
		if control.Phase == "finalising" {
			return placementError("the original prepared guard awaits its saved acknowledgement")
		}
		control.Drain, control.Nonce = "purge", args.Nonce
		control.Owner, control.OwnerRun = &owner.process, run
		if control.Phase == "" {
			control.Phase = "precheck"
		}
		if control.Report == nil {
			control.Report = &api.PurgeResult{HostID: p.hostID, Scope: p.scope, User: account.Username, UID: os.Getuid(), Arch: runtime.GOARCH, Nonce: args.Nonce, Done: []string{}, Removed: []api.PurgeItem{}, Left: []api.PurgeItem{}}
		}
		owner.report = *control.Report
		return nil
	}, true)
	return owner, err
}

func (owner *purgeOwnerV1) gate(change func(*placementControl, *os.Root) error) error {
	return owner.placement.withControlRoot(owner.ctx, func(control *placementControl, guard *os.Root) error {
		if control.HostID != owner.args.HostID || control.Nonce != owner.args.Nonce || control.Drain != "purge" || control.OwnerRun != owner.run || control.Owner == nil || *control.Owner != owner.process {
			return placementError("the removal lost its exact original control owner")
		}
		if err := change(control, guard); err != nil {
			return err
		}
		if control.Drain == "purge" && control.Nonce == owner.args.Nonce {
			control.Report = &owner.report
		}
		return nil
	}, true)
}

func (owner *purgeOwnerV1) release() error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(owner.ctx), 5*time.Second)
	defer cancel()
	return owner.placement.withControlRoot(ctx, func(control *placementControl, guard *os.Root) error {
		if control.Phase == "finalising" {
			return nil
		}
		if control.OwnerRun == owner.run && control.Owner != nil && *control.Owner == owner.process {
			control.Owner, control.OwnerRun = nil, ""
			return writePlacementControl(guard, *control)
		}
		return nil
	}, false)
}
