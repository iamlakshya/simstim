package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"sync"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/checkpoint"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

type nativeForkJobV1 struct {
	mu              sync.Mutex
	destinations    []*host.ForkDestinationV1
	retained        []*host.ForkDestinationV1
	acceptFailure   error
	started, closed bool
	cancel          context.CancelFunc
	done            chan struct{}
	err             error
	agentID         string
	agentKind       string
	selected        string
}

func (s *Supervisor) nativeForkLaneV1(client *client, scope *nativeStopScopeV1, frame proto.Frame, request proto.Request) bool {
	if request.Op != proto.NativeForkDestinationV1 && request.Op != proto.NativeForkCopyV1 {
		return false
	}
	if err := scope.checkCurrentV1(scope.lifetime); err != nil {
		_ = scope.line.RejectAncillary()
		client.reply(request.ID, nil, toProtoErr(err))
		return true
	}
	scope.mu.Lock()
	job := scope.fork
	if job == nil {
		job = &nativeForkJobV1{}
		scope.fork = job
	}
	scope.mu.Unlock()
	if request.Op == proto.NativeForkDestinationV1 {
		job.mu.Lock()
		defer job.mu.Unlock()
		// The public protocol already bounds one child batch by ForkMaxCount.
		// A failed partial keeps this same batch cleanup-only until its real
		// borrowers join; it does not introduce a lifetime retry ceiling.
		if job.started || job.closed || job.acceptFailure != nil || len(job.destinations) >= api.ForkMaxCount {
			_ = scope.line.RejectAncillary()
			client.reply(request.ID, nil, toProtoErr(execRefusal("the original fork destination batch was consumed or is full")))
			return true
		}
		scope.mu.Lock()
		source := scope.operation
		scope.mu.Unlock()
		destination, err := scope.owner.AcceptForkDestinationV1(scope.lifetime, source, scope.line, frame, scope.state)
		if destination != nil {
			job.retained = append(job.retained, destination)
		}
		if err != nil {
			job.acceptFailure = err
			client.reply(request.ID, nil, toProtoErr(err))
			return true
		}
		if destination == nil {
			job.acceptFailure = execRefusal("the original fork factory returned no actual destination")
			client.reply(request.ID, nil, toProtoErr(job.acceptFailure))
			return true
		}
		for _, prior := range job.destinations {
			if prior.Child() == destination.Child() {
				job.acceptFailure = errors.Join(execRefusal("the original child reservation was already lent"), destination.Close())
				client.reply(request.ID, nil, toProtoErr(job.acceptFailure))
				return true
			}
		}
		job.destinations = append(job.destinations, destination)
		client.reply(request.ID, scope.state, nil)
		return true
	}
	if err := scope.line.RejectAncillary(); err != nil {
		client.close()
		return true
	}
	var args proto.NativeForkCopyArgsV1
	if request.ID != proto.NativeForkCopyRequestV1 || !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || args.Version != 1 || args.Scope.Version != 1 || args.Scope.Primary != scope.state.Primary || args.Scope.Nonce != scope.state.Nonce || !api.ValidSessionID(args.SourceSession) {
		client.reply(request.ID, nil, toProtoErr(execRefusal("the fork copy has another original native scope")))
		return true
	}
	job.mu.Lock()
	children := make([]string, 0, len(job.destinations))
	for _, destination := range job.destinations {
		children = append(children, destination.Child())
	}
	if job.started || job.closed || job.acceptFailure != nil || len(children) == 0 || !slices.Equal(children, args.Children) {
		job.mu.Unlock()
		client.reply(request.ID, nil, toProtoErr(execRefusal("the fork copy changed or repeated its actual destination batch")))
		return true
	}
	job.started = true
	job.selected = args.SourceSession
	ctx, cancel := context.WithCancel(scope.lifetime)
	job.cancel, job.done = cancel, make(chan struct{})
	job.mu.Unlock()
	go func() {
		defer close(job.done)
		failure := s.copyForkBatchV1(ctx, scope, job)
		// The exact destination loans end before the ACK. The outside
		// creator still holds each original OP for admission and cold start.
		failure = errors.Join(failure, job.closeDestinationsV1())
		job.mu.Lock()
		job.err = failure
		job.mu.Unlock()
		job.mu.Lock()
		agentID, agentKind := job.agentID, job.agentKind
		job.mu.Unlock()
		client.reply(request.ID, proto.NativeForkCopyResultV1{State: scope.state, AgentKind: agentKind, AgentSessionID: agentID}, toProtoErr(failure))
	}()
	return true
}

func (job *nativeForkJobV1) closeDestinationsV1() error {
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.closed {
		return nil
	}
	var failure error
	for _, destination := range job.retained {
		failure = errors.Join(failure, destination.Close())
	}
	if failure == nil {
		job.closed = true
	}
	return failure
}

func (scope *nativeStopScopeV1) joinForkJobV1() error {
	scope.mu.Lock()
	job := scope.fork
	scope.mu.Unlock()
	if job == nil {
		return nil
	}
	job.mu.Lock()
	cancel, done := job.cancel, job.done
	job.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	// An ordinary copy failure is settled after its actual worker and
	// destination borrows joined; it must not become an uncertain loan.
	return job.closeDestinationsV1()
}

func (s *Supervisor) copyForkBatchV1(ctx context.Context, scope *nativeStopScopeV1, job *nativeForkJobV1) error {
	if err := scope.checkCurrentV1(ctx); err != nil {
		return err
	}
	scope.mu.Lock()
	operation := scope.operation
	scope.mu.Unlock()
	root := s.owner()
	selected := root.member(job.selected)
	if selected == nil {
		return execRefusal("the selected fork member has no original current controller")
	}
	selected.mu.Lock()
	selectedLive := selected.generation == scope.state.Generation && selected.launched && selected.rec.phase == host.LifeRunning && (selected == root || selected.primary == root)
	selected.mu.Unlock()
	if !selectedLive {
		return execRefusal("the selected fork member's original runtime ended or changed")
	}
	root.mu.Lock()
	state := host.CloneManifest(*root.m)
	runtime := root.sb
	root.mu.Unlock()
	if state.Files == nil {
		return execRefusal("the source sandbox has no original private file plan")
	}
	if err := state.Files.State.Validate(); err != nil {
		return err
	}
	write := func(copyOne func(context.Context, *os.Root) error) error {
		return root.withFrozenV1(ctx, func(frozen context.Context) error {
			for _, destination := range job.destinations {
				if err := frozen.Err(); err != nil {
					return err
				}
				if err := destination.WithRootV1(frozen, func(root *os.Root) error { return copyOne(frozen, root) }); err != nil {
					return err
				}
			}
			if err := scope.checkCurrentV1(frozen); err != nil {
				return err
			}
			if root.member(job.selected) != selected {
				return execRefusal("the selected fork member was replaced during its copy")
			}
			selected.mu.Lock()
			valid := selected.generation == scope.state.Generation && selected.launched && selected.rec.phase == host.LifeRunning && (selected == root || selected.primary == root)
			agentKind, agentID := selected.m.Agent.Kind, selected.m.Agent.AgentSessionID
			selected.mu.Unlock()
			if !valid {
				return execRefusal("the selected fork member ended during its frozen copy")
			}
			if agentID != "" {
				agent, known := agents.Find(agentKind)
				if !known || !agent.ValidID(agentID) {
					return execRefusal("the frozen source reported an invalid conversation identity")
				}
			}
			job.mu.Lock()
			job.agentID = agentID
			job.agentKind = agentKind
			job.mu.Unlock()
			return nil
		})
	}
	var failure error
	switch runtime.(type) {
	case vmRuntime:
		if scope.native == nil || scope.connection == nil {
			return execRefusal("the original fork VM has no native disk owner")
		}
		actual := runtime.(vmRuntime)
		own, err := scope.native.RunningAccountModeForWarmV1(ctx, actual.Sandbox, scope.connection, scope.owner, operation, scope.line)
		if err != nil {
			return err
		}
		if own {
			failure = write(func(frozen context.Context, destination *os.Root) error {
				if err := destination.Mkdir("vm", 0700); err != nil {
					return err
				}
				vm, err := destination.OpenRoot("vm")
				if err != nil {
					return err
				}
				defer vm.Close()
				file, err := cachefs.OpenFile(vm, "disk.img", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
				if err != nil {
					return err
				}
				err = scope.native.CopyForkDiskV1(frozen, scope.connection, file)
				return errors.Join(err, file.Sync(), file.Close())
			})
		} else {
			failure = scope.native.WithSnapshotDiskV1(ctx, scope.connection, func(session, vm *os.Root, disk *os.File) error {
				scope.mu.Lock()
				source := scope.operation
				scope.mu.Unlock()
				if err := host.CheckForkSnapshotRootV1(ctx, source, session); err != nil {
					return err
				}
				return write(func(frozen context.Context, destination *os.Root) error {
					if err := destination.Mkdir("vm", 0700); err != nil {
						return err
					}
					vm, err := destination.OpenRoot("vm")
					if err != nil {
						return err
					}
					defer vm.Close()
					file, err := checkpoint.CopyOwned(frozen, vm, "disk.img", disk, scope.checkCurrentV1)
					if file != nil {
						err = errors.Join(err, file.Close())
					}
					return err
				})
			})
		}
	case processSandbox:
		copyRoots := func(work, home *os.Root) error {
			return write(func(frozen context.Context, destination *os.Root) error {
				return copyForkProcessV1(frozen, work, home, destination, state.Files.State)
			})
		}
		if scope.gvisor != nil {
			failure = scope.gvisor.WithForkRootsV1(ctx, copyRoots)
		} else if scope.process != nil {
			failure = scope.process.WithForkRootsV1(ctx, copyRoots)
		} else {
			return execRefusal("the original process fork source has no native mounted file owner")
		}
	default:
		return execRefusal("the original runtime has no supported fork copy producer")
	}
	if failure != nil {
		// A failed renewal invalidates the complete batch, including children
		// whose files finished before the renewal failed. No child is launched.
		for _, destination := range job.destinations {
			cleanup := destination.WithRootV1(context.Background(), func(root *os.Root) error {
				var err error
				for _, name := range []string{"work", "state", "state-files", "vm"} {
					err = errors.Join(err, root.RemoveAll(name))
				}
				return err
			})
			failure = errors.Join(failure, cleanup)
		}
	}
	return failure
}
