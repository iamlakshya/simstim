package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/checkpoint"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

type checkpointTurnV1 struct{ session, detail string }

func checkpointKeepV1(policy *api.Policy) (int, error) {
	keep := 10
	if policy != nil && policy.Sandbox.Checkpoints != nil {
		keep = *policy.Sandbox.Checkpoints
	}
	if keep < 0 || keep > 10000 {
		return 0, host.UserError("bad_value", "sandbox.checkpoints must be between 0 and 10000", "Choose how many checkpoints to keep; 0 turns them off.")
	}
	return keep, nil
}

func (s *Supervisor) checkpointPolicyV1() (int, error) {
	root := s.owner()
	root.mu.Lock()
	defer root.mu.Unlock()
	if root.m == nil || root.m.Wall == nil || root.m.Wall.Level != "microvm" {
		return 0, host.UserError("rewind_off", "checkpoints are off: this wall has no microvm session disk", "Use a microvm on a filesystem whose FICLONE probe succeeds.")
	}
	keep, err := checkpointKeepV1(root.m.Policy)
	if err == nil && keep == 0 {
		err = host.UserError("rewind_off", "checkpoints are off: sandbox.checkpoints is 0", "Set sandbox.checkpoints to the number to keep, then start the sandbox again.")
	}
	return keep, err
}

// queueCheckpointV1 does no disk I/O in the ordered guest event reader.
// Its one slot and 30 second gate cannot let guest claims evict history quickly.
func (s *Supervisor) queueCheckpointV1(turn checkpointTurnV1) {
	root := s.owner()
	root.checkpointMu.Lock()
	defer root.checkpointMu.Unlock()
	if root.checkpointClosed {
		return
	}
	if root.checkpointQueue == nil {
		ctx, cancel := context.WithCancel(root.lifetimeContext())
		root.checkpointCancel = cancel
		root.checkpointQueue = make(chan checkpointTurnV1, 1)
		root.checkpointWG.Add(1)
		go root.checkpointLoopV1(ctx)
	}
	select {
	case root.checkpointQueue <- turn:
	default:
	}
}

func (s *Supervisor) checkpointLoopV1(ctx context.Context) {
	defer s.checkpointWG.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case turn := <-s.checkpointQueue:
			if _, err := s.checkpointPolicyV1(); err != nil {
				s.checkpointStatusV1(checkpointReasonV1(err))
				continue
			}
			at := time.Now()
			if !s.checkpointLast.IsZero() && at.Sub(s.checkpointLast) < 30*time.Second {
				continue
			}
			s.checkpointLast = at
			op, done := context.WithTimeout(ctx, 30*time.Second)
			err := s.takeCheckpointV1(op, turn, at)
			done()
			if err != nil {
				s.checkpointStatusV1(checkpointReasonV1(err))
				if s.log != nil {
					s.log.Warn("checkpoint failed", "err", err)
				}
			} else {
				s.checkpointStatusV1("checkpoints: on")
			}
		}
	}
}

func (s *Supervisor) stopCheckpointsV1() {
	s = s.owner()
	s.checkpointMu.Lock()
	s.checkpointClosed = true
	if s.checkpointCancel != nil {
		s.checkpointCancel()
	}
	s.checkpointMu.Unlock()
	s.checkpointWG.Wait()
}

func (s *Supervisor) checkpointStatusV1(status string) {
	s = s.owner()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m != nil {
		s.m.Checkpoints = api.CleanText(status, 500)
		s.dirty = true
	}
}

func checkpointReasonV1(err error) string {
	var wire *proto.Error
	if errors.As(err, &wire) {
		return wire.Message
	}
	return err.Error()
}

func (s *Supervisor) checkpointProbeV1(ctx context.Context, store checkpoint.BackendV1) error {
	s = s.owner()
	s.checkpointMu.Lock()
	probed := s.checkpointProbed
	s.checkpointMu.Unlock()
	if probed {
		return nil
	}
	if err := store.Probe(ctx); err != nil {
		return err
	}
	s.checkpointMu.Lock()
	s.checkpointProbed = true
	s.checkpointMu.Unlock()
	return nil
}

func (s *Supervisor) captureCheckpointSourceV1(ctx context.Context) (_ *host.CheckpointSourceV1, returned error) {
	s = s.owner()
	operation, err := host.BeginSessionOperation(ctx, s.id, 5*time.Second)
	if err != nil {
		return nil, err
	}
	transferred := false
	defer func() {
		if !transferred {
			returned = errors.Join(returned, operation.CloseWithError())
		}
	}()
	s.mu.Lock()
	connection := s.vmStartConnection
	live := s.m != nil && s.rec.phase == host.LifeRunning && s.launched && s.link != nil && s.ctl != nil && s.generation != ""
	s.mu.Unlock()
	if !live {
		return nil, host.UserError("sandbox_stopped", "the original sandbox no longer runs", "Resume its microvm before taking or listing checkpoints.")
	}
	source, err := host.CaptureCheckpointSourceV1(ctx, s.lifetimeContext(), operation, connection)
	if err != nil {
		return nil, err
	}
	transferred = true
	return source, nil
}

func (s *Supervisor) checkpointMetadataV1(ctx context.Context, turn checkpointTurnV1) (checkpoint.Metadata, error) {
	s = s.owner()
	metadata := checkpoint.Metadata{AgentSessionIDs: map[string]string{}, Turn: api.CleanText(turn.session+": "+turn.detail, 500)}
	list := append([]*Supervisor{s}, s.childList()...)
	for _, member := range list {
		member.mu.Lock()
		if member.m != nil {
			metadata.AgentSessionIDs[member.id] = member.m.Agent.AgentSessionID
		}
		member.mu.Unlock()
	}
	s.mu.Lock()
	ctl := s.ctl
	pulled := append([]string(nil), s.m.Pulled...)
	s.mu.Unlock()
	var refs proto.CtlRefsResultV1
	if ctl == nil {
		return metadata, snapshotFailureV1("the checkpoint has no original guest control stream", nil)
	}
	if err := ctl.request(ctx, proto.CtlRefsV1, proto.CtlRefsArgsV1{Pulled: pulled}, &refs); err != nil {
		return metadata, err
	}
	if len(refs.Refs) > 256 {
		return metadata, snapshotFailureV1("the guest refs exceed the checkpoint metadata bound", nil)
	}
	prefix := "refs/heads/" + brand.Name + "/" + s.id + "/"
	seen := map[string]bool{}
	for _, ref := range refs.Refs {
		if !strings.HasPrefix(ref.Name, prefix) || api.CleanText(ref.Name, 1024) != ref.Name || !api.ValidGitOID(ref.OID) || seen[ref.Name] {
			return metadata, snapshotFailureV1("the guest sent an invalid checkpoint branch claim", nil)
		}
		seen[ref.Name] = true
		metadata.Refs = append(metadata.Refs, checkpoint.Ref{Name: ref.Name, SHA: ref.OID})
	}
	s.mu.Lock()
	if s.m.Guest != nil {
		s.m.Guest.Refs = append([]proto.BundleRef(nil), refs.Refs...)
		s.dirty = true
	}
	s.mu.Unlock()
	return metadata, nil
}

func (s *Supervisor) takeCheckpointV1(ctx context.Context, turn checkpointTurnV1, at time.Time) (returned error) {
	s = s.owner()
	keep, err := s.checkpointPolicyV1()
	if err != nil {
		return err
	}
	source, err := s.captureCheckpointSourceV1(ctx)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, source.Close()) }()
	metadata, err := s.checkpointMetadataV1(ctx, turn)
	if err != nil {
		return err
	}
	// Probe/space checks run before freezing the guest. A transient error does
	// not turn supported filesystems off permanently.
	var freezeMS int64
	err = source.WithBackend(ctx, func(store checkpoint.BackendV1, diskMax int64) error {
		if err := store.CheckSpaceV1(ctx, diskMax); err != nil {
			return err
		}
		if err := s.checkpointProbeV1(ctx, store); err != nil {
			return err
		}
		var created checkpoint.Entry
		started := time.Now()
		copyErr := s.withFrozenV1(ctx, func(frozen context.Context) error {
			var err error
			created, err = store.Create(frozen, at, metadata, 0)
			return err
		})
		freezeMS = time.Since(started).Milliseconds()
		if copyErr != nil {
			if !created.Time.IsZero() {
				cleanup, done := context.WithTimeout(s.lifetimeContext(), time.Second)
				creator, known := store.(interface {
					DiscardCreatedV1(context.Context, checkpoint.Entry) error
				})
				if known {
					copyErr = errors.Join(copyErr, creator.DiscardCreatedV1(cleanup, created))
				} else {
					copyErr = errors.Join(copyErr, snapshotFailureV1("the actual checkpoint creator must retain and reconcile its failed copy", nil))
				}
				done()
			}
			return copyErr
		}
		return store.Trim(ctx, keep)
	})
	if err == nil {
		s.audit.write("checkpoint", map[string]any{"time": at.UTC().Format(checkpoint.TimeLayout), "claimed_turn": metadata.Turn, "freeze_ms": freezeMS})
	}
	return err
}

func (s *Supervisor) listCheckpointsV1(ctx context.Context, selected string) (_ api.CheckpointsResult, returned error) {
	result := api.CheckpointsResult{ID: selected, Checkpoints: []api.CheckpointInfo{}}
	if _, err := s.checkpointPolicyV1(); err != nil {
		return result, err
	}
	source, err := s.captureCheckpointSourceV1(ctx)
	if err != nil {
		return result, err
	}
	defer func() { returned = errors.Join(returned, source.Close()) }()
	err = source.WithBackend(ctx, func(store checkpoint.BackendV1, _ int64) error {
		if err := s.checkpointProbeV1(ctx, store); err != nil {
			return err
		}
		entries, err := store.List(ctx)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			row := api.CheckpointInfo{Name: entry.Time.Format(checkpoint.TimeLayout), CreatedAt: entry.Time}
			for _, ref := range entry.Metadata.Refs {
				row.Refs = append(row.Refs, proto.BundleRef{Name: ref.Name, OID: ref.SHA})
			}
			result.Checkpoints = append(result.Checkpoints, row)
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	metadata, err := s.checkpointMetadataV1(ctx, checkpointTurnV1{session: selected})
	if err != nil {
		return result, err
	}
	for _, ref := range metadata.Refs {
		result.CurrentRefs = append(result.CurrentRefs, proto.BundleRef{Name: ref.Name, OID: ref.SHA})
	}
	return result, result.Validate(selected)
}

func (s *Supervisor) checkpointRequestV1(c *client, req proto.Request) bool {
	if req.Op != api.OpCheckpoints && req.Op != api.OpRewind {
		return false
	}
	if req.Op == api.OpCheckpoints {
		var args api.CheckpointsArgs
		if json.Unmarshal(req.Args, &args) != nil || args.Validate() != nil || s.owner().member(args.ID) == nil {
			c.reply(req.ID, nil, toProtoErr(snapshotFailureV1("the checkpoint request names another sandbox", nil)))
			return true
		}
		ctx, done := context.WithTimeout(s.lifetimeContext(), 10*time.Second)
		defer done()
		result, err := s.owner().listCheckpointsV1(ctx, args.ID)
		c.reply(req.ID, result, toProtoErr(err))
		return true
	}
	var args api.RewindArgs
	if json.Unmarshal(req.Args, &args) != nil || args.Validate() != nil || s.owner().member(args.ID) == nil {
		c.reply(req.ID, nil, toProtoErr(snapshotFailureV1("the rewind request names another sandbox or checkpoint", nil)))
		return true
	}
	ctx, done := context.WithTimeout(s.lifetimeContext(), 10*time.Second)
	defer done()
	list, err := s.owner().listCheckpointsV1(ctx, args.ID)
	if err != nil {
		c.reply(req.ID, nil, toProtoErr(err))
		return true
	}
	found := false
	for _, entry := range list.Checkpoints {
		found = found || entry.Name == args.Checkpoint
	}
	if !found {
		c.reply(req.ID, nil, host.UserError("rewind_unknown", "the selected checkpoint is no longer available", "List the session's checkpoints and choose one that still exists."))
		return true
	}
	// A running disk borrow cannot be held across physical stop. The native
	// stopped lifecycle owner must adopt it before replacement and cold wake.
	c.reply(req.ID, nil, host.UserError("rewind_source", "rewind needs the original stopped disk owner and a fresh cold-start admission", "Keep the checkpoints and use the host's current lifecycle helper."))
	return true
}
