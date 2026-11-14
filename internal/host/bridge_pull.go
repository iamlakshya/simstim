package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

func init() { Features = append(Features, api.FeaturePullBundle) }

type bridgePull struct {
	*PullRPC
	routes   map[string]*pullRoute
	aliases  map[string]*pullRoute
	pending  map[uint64]*pullPending
	relays   map[string]*LivePullRelay
	delivery sync.Mutex
}

func (b *bridge) pullOwner() (*bridgePull, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ownedOutput == nil {
		return nil, UserError("pull_owner", "the pull connection has no cancellable output owner", "Use the complete host helper entrypoint.")
	}
	if b.pullClosed {
		return nil, UserError("pull_closed", "the pull connection has ended", "Reconnect before starting another pull.")
	}
	if b.pull != nil {
		return b.pull, nil
	}
	rpc, err := NewPullRPC(context.Background(), openPullSource)
	if err != nil {
		return nil, err
	}
	b.pull = &bridgePull{PullRPC: rpc, routes: map[string]*pullRoute{}, aliases: map[string]*pullRoute{}, pending: map[uint64]*pullPending{}, relays: map[string]*LivePullRelay{}}
	// The actual owner has one physical waiter and a broadcast done latch.
	// Lost output ends exports even while the caller keeps input open.
	go func() { _ = b.ownedOutput.Wait(); b.closePull() }()
	return b.pull, nil
}

func (b *bridge) closePull() {
	b.mu.Lock()
	b.pullClosed = true
	owner := b.pull
	var relays []*LivePullRelay
	if owner != nil {
		for _, relay := range owner.relays {
			relays = append(relays, relay)
		}
	}
	b.mu.Unlock()
	if b.ownedOutput != nil {
		_ = b.ownedOutput.Cancel()
	}
	for _, relay := range relays {
		_ = relay.Close()
	}
	if owner != nil {
		owner.Close()
	}
}

// preparePull is called on the normal reader before scheduling any capture.
func (b *bridge) preparePull(req proto.Request) (func(context.Context) (any, error), context.Context, error) {
	owner, err := b.pullOwner()
	if err != nil {
		return nil, nil, err
	}
	// Flush precedes the routing lock. An ACK may already be visible to the
	// client while its owned output helper is completing the same raw write.
	var finish api.PulledArgs
	abort := req.Op == api.OpPulled && json.Unmarshal(req.Args, &finish) == nil && finish.Abort
	if !abort {
		owner.delivery.Lock()
		owner.delivery.Unlock()
		if err := b.retireIdlePullRelays(owner); err != nil {
			return nil, nil, err
		}
	}
	return b.prepareRoutedPull(owner, req)
}

// pullSpoolDir is outside every guest grant. The canonical Source already
// holds the primary lease when the manager creates files here.
func pullSpoolDir(primary string) (string, error) {
	if !api.ValidSessionID(primary) {
		return "", UserError("pull_protocol", "the pull primary is invalid", "List sessions and use their full address.")
	}
	root, err := os.OpenRoot(paths.SessionsDir())
	if err != nil {
		return "", err
	}
	defer root.Close()
	info, err := root.Lstat(primary)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", UserError("pull_store", "the protected pull folder is unavailable", "Check the session storage before pulling.")
	}
	session, err := root.OpenRoot(primary)
	if err != nil {
		return "", err
	}
	defer session.Close()
	if err = session.Mkdir("pull", 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	info, err = session.Lstat("pull")
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return "", UserError("pull_store", "the protected pull folder is unsafe", "Remove the unsafe pull spool entry and try again.")
	}
	return filepath.Join(paths.SessionDir(primary), "pull"), nil
}

func (b *bridge) handlePull(req proto.Request) bool {
	if !IsPullOp(req.Op) {
		return false
	}
	run, ctx, err := b.preparePull(req)
	if err != nil {
		b.replyPull(req.ID, nil, err)
		return true
	}
	if run == nil {
		return true
	}
	result, err := run(ctx)
	b.replyPull(req.ID, result, err)
	return true
}

func (b *bridge) replyPull(id uint64, value any, err error) {
	if err != nil {
		err = asUserError(err, "pull_failed", "Check the session's storage and runtime, then pull again.")
	}
	b.mu.Lock()
	owner := b.pull
	b.mu.Unlock()
	if owner != nil {
		owner.delivery.Lock()
	}
	response, writeErr := b.replyFrame(id, value, err, b.bindPullProgress)
	if writeErr == nil {
		b.observePull(response)
	}
	if owner != nil {
		owner.delivery.Unlock()
	}
	if writeErr != nil {
		b.closePull()
	}
}
