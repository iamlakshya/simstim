package supervisor

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/inputtransfer"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/phaseclock"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (s *Supervisor) inputFrame(c *client, reader *bufio.Reader, frame proto.Frame, first bool) bool {
	if frame.Type != proto.TRequest {
		return false
	}
	var request proto.Request
	if json.Unmarshal(frame.Payload, &request) != nil || (request.Op != "inputs.begin" && request.Op != inputtransfer.Describe) {
		return false
	}
	if !first {
		c.reply(request.ID, nil, inputFailure("inputs need a dedicated connection"))
		return true
	}
	if request.Op == inputtransfer.Describe {
		result, err := s.owner().describeInputOperation(request.Args)
		var failure *proto.Error
		if err != nil {
			failure = inputOperationError(err)
		}
		c.reply(request.ID, result, failure)
		return true
	}
	if err := s.servePendingInputs(c, reader, request); err != nil {
		c.reply(request.ID, nil, inputOperationError(err))
	}
	return true
}

func (s *Supervisor) servePendingInputs(c *client, reader *bufio.Reader, request proto.Request) error {
	begin, err := decodeInputBegin(request.Args)
	if err != nil {
		return err
	}
	root := s.owner()
	if begin.Session != root.id || begin.Summary == nil || !begin.FinishOnly && len(begin.Summary.Repositories) != 0 {
		return inputFailure("pending copies need their fresh copy-only declaration")
	}
	ctx, cancel := phaseclock.WithDeadline(root.lifetimeContext(), now().Add(inputHandshakeLimit))
	defer cancel()
	operation, err := host.BeginSessionOperation(ctx, begin.Session, 5*time.Second)
	if err != nil {
		return err
	}
	defer operation.Close()
	if begin.FinishOnly {
		if !operation.Valid(root.id) {
			return inputFailure("the input completion operation changed")
		}
		if err := root.recoverInputFinish(ctx, begin); err != nil {
			return err
		}
		c.reply(request.ID, inputtransfer.BeginResult{Version: 1, RunGeneration: root.generation, Finished: true}, nil)
		return c.writeErr
	}
	root.mu.Lock()
	valid := root.rec.phase == host.LifeRunning && root.m.CopiesPending && (root.m.Inputs == nil || root.m.Inputs.Nonce != begin.Nonce)
	generation := root.generation
	copyMax, seedMax, limitsErr := host.SessionInputLimits(root.m)
	root.mu.Unlock()
	if limitsErr != nil {
		return limitsErr
	}
	if err := begin.Summary.Validate(copyMax, seedMax); err != nil {
		return err
	}
	if !valid || !operation.Valid(root.id) {
		return inputFailure("the current run has no matching pending copy phase")
	}
	clock, err := newInputPhaseClock(root.lifetimeContext(), *begin.Summary)
	if err != nil {
		return err
	}
	defer clock.close()
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(clock.handshake) {
		clock.handshake = deadline
	}
	if err := root.requireInputGuestMode(ctx, true); err != nil {
		return err
	}
	if err := root.retireFailedInput(ctx); err != nil {
		return err
	}
	root.saveMu.Lock()
	var snapshot host.Manifest
	err = root.writeManifestChange(&snapshot, false, nil, nil, func(m *host.Manifest) error {
		if m.Run.Generation != generation || !m.CopiesPending {
			return inputFailure("the pending copy run changed")
		}
		m.Inputs = host.CloneInputSummary(begin.Summary)
		return nil
	}, func(m *host.Manifest) { root.m.Inputs = host.CloneInputSummary(m.Inputs) })
	root.saveMu.Unlock()
	if err != nil {
		return err
	}
	input := &startupInput{clock: clock, scope: begin.Scope, summary: *host.CloneInputSummary(begin.Summary), copyMax: copyMax, seedMax: seedMax, runGeneration: generation}
	root.mu.Lock()
	root.lateInput = input
	root.mu.Unlock()
	c.reply(request.ID, inputtransfer.BeginResult{Version: 1, RunGeneration: generation}, nil)
	if c.writeErr != nil {
		return c.writeErr
	}
	_ = c.conn.SetDeadline(time.Time{})
	input.carrier = link.ContinueInputs(c.conn, reader, link.Supervisor, false)
	defer input.carrier.Close()
	_ = root.applyInputPhase(root.lifetimeContext(), input, nil)
	return nil
}

func decodeInputBegin(raw []byte) (inputtransfer.BeginArgs, error) {
	var begin inputtransfer.BeginArgs
	fields, err := api.JSONObjectFields(raw, "session", "nonce", "digest", "summary", "finish_only", "run_generation")
	if err != nil || !api.ExactJSONFields(raw, inputtransfer.BeginArgs{}) || json.Unmarshal(raw, &begin) != nil || !api.ValidSessionID(begin.Session) || !begin.Nonce.Valid() {
		return begin, inputFailure("invalid input begin scope")
	}
	for _, name := range []string{"finish_only", "run_generation"} {
		if value, present := fields[name]; present && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return begin, inputFailure("input completion metadata cannot be null")
		}
	}
	if _, present := fields["summary"]; present && begin.Summary == nil {
		return begin, inputFailure("the input summary cannot be null")
	}
	if begin.FinishOnly && (begin.Summary == nil || begin.RunGeneration == "") || !begin.FinishOnly && begin.RunGeneration != "" {
		return begin, inputFailure("the input completion recovery has no exact original run")
	}
	if begin.Summary != nil {
		if !api.ExactJSONFields(fields["summary"], api.InputSummary{}) || begin.Summary.Validate(math.MaxInt64, math.MaxInt64) != nil || begin.Summary.Nonce != begin.Nonce || begin.Summary.Digest != begin.Digest {
			return begin, inputFailure("the pending copy declaration differs from its scope")
		}
	}
	return begin, nil
}

func inputOperationError(err error) *proto.Error {
	if err == nil {
		return nil
	}
	var failure *proto.Error
	if errors.As(err, &failure) {
		return failure
	}
	return &proto.Error{Code: "input_protocol", Message: "the owned input operation could not finish: " + api.CleanText(err.Error(), api.BlockMax), Hint: "Keep the original local operation receipt and retry its completion. If the transfer was partial, prepare a fresh authorised copy operation."}
}
