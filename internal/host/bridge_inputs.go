package host

import (
	"bufio"
	"encoding/json"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/inputtransfer"
	"github.com/iamlakshya/simstim/internal/proto"
)

// inputs transfers one dedicated, authenticated input continuation. It owns
// no payload store, process or listener; the actual supervisor owns the phase.
func (b *bridge) inputs(r *bufio.Reader, req proto.Request) error {
	var begin inputtransfer.BeginArgs
	describe := req.Op == inputtransfer.Describe
	if describe {
		var operation inputtransfer.OperationArgs
		if !api.ExactJSONFields(req.Args, operation) || json.Unmarshal(req.Args, &operation) != nil || operation.RunGeneration == "" {
			err := UserError("bad_request", "invalid original input operation fields", "Use the original local input operation receipt.")
			b.reply(req.ID, nil, err)
			return err
		}
		begin.Scope, begin.RunGeneration = operation.Scope, operation.RunGeneration
	} else {
		if !api.ExactJSONFields(req.Args, inputtransfer.BeginArgs{}) {
			err := UserError("bad_request", "invalid input begin fields", "Prepare the local input request again.")
			b.reply(req.ID, nil, err)
			return err
		}
		if err := json.Unmarshal(req.Args, &begin); err != nil {
			b.reply(req.ID, nil, err)
			return err
		}
	}
	if !api.ValidSessionID(begin.Session) || !begin.Nonce.Valid() {
		err := UserError("bad_request", "invalid input scope", "Prepare the local input request again.")
		b.reply(req.ID, nil, err)
		return err
	}
	m, err := LoadManifest(begin.Session)
	if err != nil {
		b.reply(req.ID, nil, err)
		return err
	}
	if m.Wall == nil {
		err = UserError("not_walled", "this session has no sandbox for input", "Use shell for the deprecated unwalled terminal, or start a walled session.")
		b.reply(req.ID, nil, err)
		return err
	}
	if m.Life != nil && m.Life.Phase == LifeStopped {
		err = UserError("sandbox_stopped", "the sandbox is stopped", "Resume its session, then run input again.")
		b.reply(req.ID, nil, err)
		return err
	}
	line, hello, err := dialSessionHolder(begin.Session, 10*time.Second)
	if err != nil {
		b.reply(req.ID, nil, err)
		return err
	}
	defer line.c.Close()
	required := []string{inputtransfer.Feature}
	if describe || begin.FinishOnly {
		required = append(required, inputtransfer.FinishFeature)
	}
	if err = requireSupervisorFeatures(hello, required...); err != nil {
		b.reply(req.ID, nil, err)
		return err
	}
	if err = line.w.WriteJSON(proto.TRequest, req); err != nil {
		b.reply(req.ID, nil, err)
		return err
	}
	frame, err := proto.Read(line.r)
	if err != nil {
		b.reply(req.ID, nil, err)
		return err
	}
	var resp proto.Response
	if frame.Type != proto.TResponse || json.Unmarshal(frame.Payload, &resp) != nil || resp.ID != req.ID {
		err = UserError("protocol", "the supervisor sent an invalid input answer", "Stop and resume the session.")
		b.reply(req.ID, nil, err)
		return err
	}
	if resp.OK && (describe || begin.FinishOnly) {
		valid := false
		if describe {
			var result inputtransfer.DescribeResult
			valid = api.ExactJSONFields(resp.Result, result) && json.Unmarshal(resp.Result, &result) == nil && result.Version == 1 && result.RunGeneration == begin.RunGeneration && result.Summary.Nonce == begin.Nonce && result.Summary.Digest == begin.Digest
		} else {
			var result inputtransfer.BeginResult
			valid = api.ExactJSONFields(resp.Result, result) && json.Unmarshal(resp.Result, &result) == nil && result.Version == 1 && result.Finished && result.RunGeneration == begin.RunGeneration
		}
		if !valid {
			err = UserError("protocol", "the supervisor returned an invalid input operation receipt", "Keep the original local operation receipt and check the current helper.")
			b.reply(req.ID, nil, err)
			return err
		}
	}
	if err = b.w.Write(proto.TResponse, frame.Payload); err != nil {
		return err
	}
	if !resp.OK || describe || begin.FinishOnly {
		return nil
	}
	_ = line.c.SetDeadline(time.Time{})
	done := make(chan error, 1)
	go func() { done <- proto.RelayStreams(line.r, b.w); line.c.Close() }()
	// The main bridge reader remains the only reader of its borrowed input.
	// A final client close ends this loop; no flags/deadlines on stdin change.
	err = proto.RelayStreams(r, line.w)
	line.c.Close()
	<-done
	return err
}
