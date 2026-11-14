package supervisor

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/pullexport"
)

type guestStreamOpener func(context.Context, link.Kind, any) (*link.Stream, error)

// OpenPullSource is the actual stopped/unwalled bridge resource consumer. A
// live sandbox is handled on its retained supervisor line without a bridge lease.
func OpenPullSource(ctx context.Context, original, expectedPrimary string) (pullexport.Source, error) {
	operation, err := host.BeginSessionOperation(ctx, original, 5*time.Second)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			operation.Close()
		}
	}()
	if operation.Primary() != expectedPrimary {
		return nil, pullChanged()
	}
	m, err := operation.Load()
	if err != nil {
		return nil, err
	}
	if m.Wall == nil {
		source, err := host.NewUnwalledPullSource(m, func() error { operation.Close(); return nil })
		keep = err == nil
		return source, err
	}
	if host.SessionRunning(m) {
		return nil, &proto.Error{Code: "sandbox_changed", Message: "the sandbox is now running", Hint: "Repeat pull through its live supervisor connection."}
	}
	guest, err := host.OpenStoppedGuest(ctx, operation)
	if err != nil {
		return nil, err
	}
	var closeOnce sync.Once
	var closeErr error
	closeOwned := func() error {
		closeOnce.Do(func() {
			closeErr = guest.Close() // actual owned Kill/Wait before op.lock release
			operation.Close()
		})
		return closeErr
	}
	source, err := guestPullSource(m, m, guest.Call, guest.Open, func(ctx context.Context, commits []string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		fresh, err := operation.Load()
		if err != nil {
			return err
		}
		if err := host.ApplyPulled(fresh, commits); err != nil {
			return err
		}
		return fresh.Save()
	}, closeOwned)
	if err != nil {
		return nil, errors.Join(err, closeOwned())
	}
	keep = true
	return source, nil
}

func (s *Supervisor) openPullSource(ctx context.Context, original, expectedPrimary string) (pullexport.Source, error) {
	root := s.owner()
	operation, err := host.BeginSessionOperation(ctx, original, 5*time.Second)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			operation.Close()
		}
	}()
	if operation.Primary() != expectedPrimary || expectedPrimary != root.id {
		return nil, pullChanged()
	}
	fresh, err := operation.Load()
	if err != nil {
		return nil, err
	}
	root.mu.Lock()
	snapshot := root.snapshot()
	active := root.link != nil && root.ctl != nil && root.runtimeCleaned != nil && root.rec.phase == host.LifeRunning && fresh.Run.Generation == root.generation && fresh.Run.Generation != ""
	root.mu.Unlock()
	if !active || fresh.Wall == nil {
		return nil, pullChanged()
	}
	var cancelMu sync.Mutex
	cancelFailed := false
	call := func(ctx context.Context, op string, args, out any) error {
		err := root.ctl.requestDetached(ctx, op, args, out)
		if op == proto.CtlBundleCancel && err != nil {
			cancelMu.Lock()
			cancelFailed = true
			cancelMu.Unlock()
		}
		return err
	}
	closeOwned := func() error {
		cancelMu.Lock()
		uncertain := cancelFailed
		cancelMu.Unlock()
		if uncertain {
			// Cancellation after a fully sent prepare never stops the sandbox.
			// A failed explicit cleanup ACK is different: hold the primary until
			// the existing runtime owner has established actual cleanup.
			root.loseLink("the owned bundle command did not confirm cleanup")
			<-root.runtimeCleaned
		}
		operation.Close()
		if uncertain {
			root.mu.Lock()
			defer root.mu.Unlock()
			return root.runtimeCleanupErr
		}
		return nil
	}
	source, err := guestPullSource(&snapshot, fresh, call, root.link.Open, func(ctx context.Context, commits []string) error {
		return root.recordPulled(ctx, operation, commits)
	}, closeOwned)
	if err != nil {
		return nil, err
	}
	keep = true
	return source, nil
}

func guestPullSource(m, protected *host.Manifest, call func(context.Context, string, any, any) error, open guestStreamOpener, record func(context.Context, []string) error, closeOwned func() error) (pullexport.Source, error) {
	// Policy/history come from the fresh protected record under the SAME
	// operation lease. Live Cwd/link/epoch remain the actual runtime owner's.
	seed, err := host.ProtectedSeedHeads(protected)
	if err != nil {
		return nil, err
	}
	limit, err := host.EffectivePullMax(protected)
	if err != nil {
		return nil, err
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	id := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(nonce[:]))
	options := pullexport.GuestOptions{ID: id, Primary: m.ID, Cwd: m.Cwd, Seeds: seed, PullMax: limit, Call: call, Open: open, Record: record, Close: closeOwned}
	if protected.Files != nil {
		repositories, _, err := protected.Files.NormaliseRepositories()
		if err != nil {
			return nil, err
		}
		for _, repository := range repositories {
			if repository.Scratch {
				continue
			}
			head := protected.SeedHeads[repository.Repo]
			if head == "" {
				head = repository.Commit
			}
			if !api.ValidGitOID(head) || repository.Commit != "" && repository.Commit != head {
				return nil, pullChanged()
			}
			options.Repositories = append(options.Repositories, pullexport.GuestRepository{Name: repository.Repo, Cwd: "/work/" + repository.Repo, Seeds: []string{head}})
		}
		if len(options.Repositories) == 0 {
			return nil, &proto.Error{Code: "pull_repo", Message: "the sandbox has no protected Git repository", Hint: "Use cp for a scratch workspace, or create a sandbox from a repository."}
		}
	}
	return pullexport.NewGuestSource(options)
}

func pullChanged() error {
	return &proto.Error{Code: "sandbox_changed", Message: "the original sandbox or runtime changed", Hint: "Repeat pull using its current complete session address."}
}
