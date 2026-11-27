package host

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// LivePullRelay retains one ordinary authenticated UNIX line and forwards
// whole canonical RPC frames. Client/supervisor keep the only request maps;
// the relay has no pending-ID table, export registry or primary lease.
type LivePullRelay struct {
	hello           proto.Hello
	progressFactory func(proto.Response) func()
	progressLatest  relayProgress
	progressPending bool
	progressWake    chan struct{}
	progressQuit    chan struct{}
	progressDone    chan struct{}
	progressStop    sync.Once
	line            *holderLine
	output          *proto.Writer
	owner           OwnedBridgeOutput
	observe         func(proto.Response)
	delivery        *sync.Mutex
	send            sync.Mutex
	mu              sync.Mutex
	closed          bool
	once            sync.Once
	done            chan struct{}
}

// NewLivePullRelay requires actual cancellable output ownership. A generic
// borrowed writer cannot claim bounded relay shutdown or streaming support.
func NewLivePullRelay(primary string, output *proto.Writer, owner OwnedBridgeOutput, observe func(proto.Response), sharedDelivery ...*sync.Mutex) (*LivePullRelay, error) {
	if output == nil || owner == nil {
		return nil, UserError("pull_owner", "the live export has no owned output", "Use the complete process-stdio host entrypoint.")
	}
	line, hello, err := dialSessionHolder(primary, 3*time.Second)
	if err != nil {
		return nil, err
	}
	if err = requireSupervisorFeatures(hello, api.FeaturePullBundle, api.FeaturePullOutputProgress); err != nil {
		line.c.Close()
		return nil, err
	}
	_ = line.c.SetDeadline(time.Time{})
	delivery := &sync.Mutex{}
	if len(sharedDelivery) != 0 && sharedDelivery[0] != nil {
		delivery = sharedDelivery[0]
	}
	relay := &LivePullRelay{hello: hello, line: line, output: output, owner: owner, observe: observe, delivery: delivery, done: make(chan struct{})}
	go relay.read()
	return relay, nil
}

// Forward writes synchronously in the bridge's reader order. Abort cannot
// overtake an admitted begin on the retained normal full-peer connection.
func (r *LivePullRelay) Forward(req proto.Request) error {
	if !IsPullOp(req.Op) {
		return UserError("pull_protocol", "the relay takes only pull RPCs", "Use the existing pull operation names.")
	}
	if req.Op == api.OpPullBegin {
		var args api.PullBeginArgs
		if !api.ExactJSONFields(req.Args, args) || json.Unmarshal(req.Args, &args) != nil {
			return pullRouteError("invalid repository pull request")
		}
		if args.Repo != "" {
			if err := requireSupervisorFeatures(r.hello, api.FeaturePullRepositories); err != nil {
				return err
			}
		}
	}
	r.send.Lock()
	defer r.send.Unlock()
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return UserError("pull_closed", "the live pull connection ended", "Reconnect before another pull.")
	}
	_ = r.line.c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := r.line.w.WriteJSON(proto.TRequest, req)
	_ = r.line.c.SetWriteDeadline(time.Time{})
	if err != nil {
		r.cancel()
	}
	return err
}

func (r *LivePullRelay) read() {
	defer close(r.done)
	defer r.cancel()
	for {
		frame, err := proto.Read(r.line.r)
		if err != nil || frame.Type != proto.TResponse {
			return
		}
		r.mu.Lock()
		factory := r.progressFactory
		r.mu.Unlock()
		var response proto.Response
		if (r.observe != nil || factory != nil) && json.Unmarshal(frame.Payload, &response) != nil {
			return
		}
		var progress func()
		if factory != nil {
			progress = factory(response)
		}
		r.delivery.Lock()
		err = r.output.WriteProgress(proto.TResponse, frame.Payload, progress)
		if err == nil && r.observe != nil {
			r.observe(response) // AFTER actual successful bounded frame delivery
		}
		r.delivery.Unlock()
		if err != nil {
			return
		}
	}
}
func (r *LivePullRelay) cancel() {
	r.once.Do(func() {
		r.mu.Lock()
		r.closed = true
		r.mu.Unlock()
		// Cancel actual output before joining a writer blocked there.
		_ = r.owner.Cancel()
		_ = r.line.c.Close()
		r.signalProgressStop()
	})
}

// Close cancels output and the owned retained line, then joins its one reader.
// The supervisor joins domain jobs before releasing their primary leases.
func (r *LivePullRelay) Close() error {
	if r != nil {
		_ = r.owner.Cancel()
		r.cancel()
		<-r.done
		r.joinProgress()
	}
	return nil
}

// Flush waits for actual frame delivery and its observer. Call it before the
// routing mutex: the observer may need that mutex to release the active slot.
func (r *LivePullRelay) Flush() {
	r.delivery.Lock()
	r.delivery.Unlock()
}

// RetireIdle closes only this owned line and joins its reader. The caller
// owns routing admission and has proved no active export or pending response.
// This is called from the bridge reader, never the relay's observer itself.
func (r *LivePullRelay) RetireIdle() error {
	r.Flush()
	r.send.Lock()
	r.once.Do(func() {
		r.mu.Lock()
		r.closed = true
		r.mu.Unlock()
		_ = r.line.c.Close() // expected idle retirement does not cancel output
		r.signalProgressStop()
	})
	r.send.Unlock()
	<-r.done
	r.joinProgress()
	return nil
}
