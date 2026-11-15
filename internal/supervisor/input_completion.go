package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/inputtransfer"
	"github.com/iamlakshya/simstim/internal/proto"
)

// inputCompletion retains one same-run operation, never file contents or
// laptop source authority. Only canonical SenderEOF may set senderDone.
type inputCompletion struct {
	mu                              sync.Mutex
	spool                           *os.File
	name                            string
	identity                        os.FileInfo
	closed, removed                 bool
	senderDone, guestDone, finished bool
	startAttempted                  bool
}

func newInputCompletion(spool *os.File) (*inputCompletion, error) {
	identity, err := spool.Stat()
	if err != nil {
		return nil, err
	}
	return &inputCompletion{spool: spool, name: spool.Name(), identity: identity}, nil
}
func (c *inputCompletion) markSenderEOF()   { c.mu.Lock(); c.senderDone = true; c.mu.Unlock() }
func (c *inputCompletion) canRecover() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.senderDone }
func (c *inputCompletion) cleanup() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var closeErr, removeErr error
	if !c.closed {
		// os.File consumes its descriptor even when Close returns an error. Never
		// close a reused descriptor or replay that error forever on a new retry.
		c.closed = true
		closeErr = c.spool.Close()
	}
	if !c.removed {
		current, err := os.Lstat(c.name)
		switch {
		case errors.Is(err, os.ErrNotExist):
			c.removed = true
		case err != nil:
			removeErr = err
		case !os.SameFile(c.identity, current):
			removeErr = inputFailure("the owned input metadata identity changed before cleanup")
		default:
			removeErr = os.Remove(c.name)
			if removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
				c.removed = true
				removeErr = nil
			}
		}
	}
	return errors.Join(closeErr, removeErr)
}
func (s *Supervisor) finishInput(ctx context.Context, input *startupInput, guestComplete bool) error {
	receipt := input.completion
	if receipt == nil || !receipt.canRecover() {
		return inputFailure("the input operation has no canonical sender completion")
	}
	receipt.mu.Lock()
	if guestComplete {
		receipt.guestDone = true
	}
	confirmed := receipt.guestDone
	receipt.mu.Unlock()
	if !confirmed {
		return inputFailure("the input guest completion has not been joined")
	}
	if err := receipt.cleanup(); err != nil {
		return err
	}
	if err := s.commitInputCompletion(ctx, input); err != nil {
		return err
	}
	receipt.mu.Lock()
	receipt.finished = true
	receipt.mu.Unlock()
	return nil
}
func (s *Supervisor) recoverInputFinish(ctx context.Context, begin inputtransfer.BeginArgs) error {
	s.mu.Lock()
	input := s.retainedInputLocked(begin.Scope,begin.RunGeneration)
	valid := s.rec.phase == host.LifeRunning && begin.RunGeneration == s.generation && input != nil && input.runGeneration == s.generation && input.scope == begin.Scope && begin.Summary != nil && input.summary.Equal(*begin.Summary) && s.m.Inputs != nil && s.m.Inputs.Equal(input.summary) && s.m.Run.Generation == s.generation
	s.mu.Unlock()
	if !valid || input.completion == nil || !input.completion.canRecover() {
		return inputFailure("no exact current-run completed input operation can be recovered")
	}
	input.completion.mu.Lock()
	complete, terminal := input.completion.guestDone, input.completion.finished
	input.completion.mu.Unlock()
	if terminal {
		s.mu.Lock()
		cleared := !s.m.CopiesPending
		s.mu.Unlock()
		if cleared {
			return nil
		}
	}
	if !complete {
		status, err := s.inputGuestStatus(ctx, input)
		if err != nil {
			return err
		}
		if !status.Found || !status.Joined || !status.Complete {
			return inputFailure("the matching input guest has not completed this operation")
		}
	}
	return s.finishInput(ctx, input, true)
}
func (s *Supervisor) closeLateInput() error {
	s.mu.Lock()
	first,late:=s.startupInput,s.lateInput
	s.mu.Unlock()
	var result error
	for index,input:=range []*startupInput{first,late} {
		if input!=nil && input.completion!=nil && (index==0 || input!=first){result=errors.Join(result,input.completion.cleanup())}
	}
	return result
}

func (s *Supervisor) inputGuestStatus(ctx context.Context, input *startupInput) (inputtransfer.StatusResult, error) {
	var raw json.RawMessage
	if err := s.ctl.requestDetached(ctx, inputtransfer.CtlStatus, input.scope, &raw); err != nil {
		return inputtransfer.StatusResult{}, err
	}
	var status inputtransfer.StatusResult
	if !api.ExactJSONFields(raw, status) || json.Unmarshal(raw, &status) != nil || status.Version != 1 || status.Scope != input.scope || status.Complete && (!status.Found || !status.Joined) {
		return status, inputFailure("the matching guest input status is invalid")
	}
	return status, nil
}
func (s *Supervisor) retireFailedInput(ctx context.Context) error {
	s.mu.Lock()
	prior := s.lateInput
	s.mu.Unlock()
	if prior == nil || prior.completion == nil {
		return nil
	}
	receipt := prior.completion
	receipt.mu.Lock()
	attempted, complete := receipt.startAttempted, receipt.guestDone
	receipt.mu.Unlock()
	if complete {
		return inputFailure("finish the original completed input operation before starting another")
	}
	if attempted {
		status, err := s.inputGuestStatus(ctx, prior)
		if err != nil {
			return err
		}
		if !status.Joined {
			return inputFailure("the previous input operation is still joining")
		}
		if status.Complete {
			return inputFailure("finish the original completed input operation before starting another")
		}
	}
	return receipt.cleanup()
}

func (s *Supervisor) describeInputOperation(raw []byte) (*inputtransfer.DescribeResult, error) {
	var args inputtransfer.OperationArgs
	if !api.ExactJSONFields(raw, args) || json.Unmarshal(raw, &args) != nil || !api.ValidSessionID(args.Session) || !args.Nonce.Valid() || args.RunGeneration == "" {
		return nil, inputFailure("invalid original input operation identity")
	}
	s.mu.Lock()
	input := s.retainedInputLocked(args.Scope,args.RunGeneration)
	valid := s.rec.phase == host.LifeRunning && args.RunGeneration == s.generation && input != nil && input.runGeneration == s.generation && input.scope == args.Scope && s.m.Inputs != nil && s.m.Inputs.Equal(input.summary) && s.m.Run.Generation == s.generation
	var summary *api.InputSummary
	if valid {
		summary = host.CloneInputSummary(&input.summary)
	}
	s.mu.Unlock()
	if !valid {
		return nil, inputFailure("no exact original input operation is retained in this run")
	}
	result := &inputtransfer.DescribeResult{Version: 1, RunGeneration: args.RunGeneration, Summary: *summary}
	payload, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if err := proto.NewWriter(io.Discard).WriteJSON(proto.TResponse, proto.Response{ID: math.MaxUint64, OK: true, Result: payload}); err != nil {
		return nil, inputFailure("the original input operation summary exceeds its whole response bound")
	}
	return result, nil
}
