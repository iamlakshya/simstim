package guest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/inputtransfer"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

type inputPhase struct {
	footprint                                      inputtransfer.StagingFootprint
	late                                           bool
	control                                        inputtransfer.Control
	ctx                                            context.Context
	cancel                                         context.CancelFunc
	put                                            chan *link.Stream
	bundle                                         chan *link.Stream
	done                                           chan struct{}
	streams                                        []*link.Stream // Init.mu protects current ownership and phase state
	putTaken, bundleExpected, bundleTaken, success bool
}

func inputError(message string) *proto.Error {
	return &proto.Error{Code: "input_protocol", Message: message, Hint: "Prepare the local inputs again with matching client and helper versions."}
}

func decodeInputPrepare(raw []byte) (inputtransfer.PrepareArgs, error) {
	var args inputtransfer.PrepareArgs
	fields, err := api.JSONObjectFields(raw, "control", "start")
	if err != nil || !api.ExactJSONFields(raw, inputtransfer.PrepareArgs{}) || len(fields["control"]) == 0 {
		return args, inputError("invalid input preparation")
	}
	if _, err = api.JSONObjectFields(fields["control"], "session", "nonce", "digest", "copy_max", "seed_max", "protected"); err != nil {
		return args, inputError("invalid input control fields")
	}
	if value := fields["start"]; len(value) > 0 {
		kind := reflect.TypeFor[proto.CtlStartArgs]()
		var keys []string
		for index := range kind.NumField() {
			name := strings.Split(kind.Field(index).Tag.Get("json"), ",")[0]
			if name != "" && name != "-" {
				keys = append(keys, name)
			}
		}
		start, err := api.JSONObjectFields(value, keys...)
		if err != nil || len(start["secrets"]) != 0 || len(start["secret_modes"]) != 0 || len(start["prepared_claim"]) != 0 || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return args, inputError("input preparation cannot expose secrets")
		}
	}
	if json.Unmarshal(raw, &args) != nil {
		return args, inputError("invalid input preparation JSON")
	}
	if _, err := inputtransfer.EncodeControl(args.Control); err != nil {
		return args, err
	}
	if a := args.Start; a != nil && (a.Session != args.Control.Session || len(a.Argv) != 0 || len(a.Hooks) != 0 || len(a.Secrets) != 0 || a.RunToken != "" || a.InputNonce != "" || a.InputDigest != "") {
		return args, inputError("input preparation cannot launch a member")
	}
	if args.Start != nil {
		if err := validatePackageManagerWire(fields["start"], *args.Start); err != nil {
			return args, err
		}
	}
	return args, nil
}

func (g *Init) prepareInputs(raw []byte) error {
	args, err := decodeInputPrepare(raw)
	if err != nil {
		return err
	}
	g.mu.Lock()
	if g.lifetime == nil || g.ended || g.starting || inputPhaseActive(g.inputs) || g.inputs != nil && (g.inputs.success || g.inputStart != nil) || args.Start == nil && !g.sessionPrepared || args.Start != nil && (g.sessionPrepared || g.holder != nil) {
		g.mu.Unlock()
		return inputError("the guest cannot reserve this input phase")
	}
	if g.primary != "" && g.primary != args.Control.Session {
		g.mu.Unlock()
		return inputError("input preparation names another controller")
	}
	g.starting = true
	if args.Start != nil {
		g.primary, g.bootAdmission = args.Start.Session, true
	}
	g.mu.Unlock()
	if args.Start != nil {
		original := *args.Start
		original.Decoys=proto.CloneDecoyFilesV1(original.Decoys)
		original.PackageManagers = proto.ClonePackageManagersV1(original.PackageManagers)
		original.Env, original.StateFiles = slices.Clone(original.Env), slices.Clone(original.StateFiles)
		err = g.prepareSession(args.Start)
		if err == nil {
			g.mu.Lock()
			g.inputStart, g.inputEnv = &original, slices.Clone(args.Start.Env)
			g.mu.Unlock()
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.starting = false
	if err != nil {
		return err
	}
	if g.ended {
		return inputError("the guest ended during input preparation")
	}
	if g.lifetime == nil {
		return inputError("the input phase has no guest lifetime")
	}
	ctx, cancel := context.WithCancel(g.lifetime)
	op := &inputPhase{late: args.Start == nil, control: args.Control, ctx: ctx, cancel: cancel, put: make(chan *link.Stream, 1), bundle: make(chan *link.Stream, 1), done: make(chan struct{})}
	op.control.Protected = slices.Clone(args.Control.Protected)
	g.inputs = op
	g.commandHolds++
	go g.runInputs(op)
	return nil
}

func (g *Init) acceptInputStream(st *link.Stream) bool {
	var header inputtransfer.Header
	if st.Header(&header) != nil || header.Purpose != "inputs" {
		return false
	}
	if !api.ExactJSONFields(st.HeaderBytes(), struct {
		Kind link.Kind `json:"kind"`
		inputtransfer.Header
	}{}) {
		_ = st.Close()
		return true
	}
	g.mu.Lock()
	op := g.inputs
	valid := op != nil && !g.ended && op.ctx.Err() == nil && !op.success && header.Scope == op.control.Scope
	var channel chan *link.Stream
	if valid {
		switch st.Kind() {
		case link.KindPut:
			valid = !op.putTaken
			if valid {
				op.putTaken, channel = true, op.put
			}
		case link.KindBundle:
			valid = op.bundleExpected && !op.bundleTaken && len(op.streams) > 0 && op.streams[0].PeerWriteClosed()
			if valid {
				op.bundleTaken, channel = true, op.bundle
			}
		default:
			valid = false
		}
	}
	if valid {
		op.streams = append(op.streams, st)
		channel <- st
	}
	g.mu.Unlock()
	if !valid {
		_ = st.Close()
	}
	return true
}

func (g *Init) runInputs(op *inputPhase) {
	defer close(op.done)
	defer func() {
		op.cancel()
		g.mu.Lock()
		streams := slices.Clone(op.streams)
		g.commandHolds--
		g.mu.Unlock()
		for _, stream := range streams {
			_ = stream.Close()
		}
		g.endIfEmpty()
	}()
	var put *link.Stream
	select {
	case put = <-op.put:
	case <-op.ctx.Done():
		return
	case <-g.sess.Done():
		return
	}
	if err := g.receiveInputs(op, put); err != nil {
		_ = put.SetWriteDeadline(time.Now().Add(ctlCloseWait))
		var failure *proto.Error
		if !errors.As(err, &failure) {
			failure = inputError("the declared input phase failed")
		}
		_ = proto.NewWriter(put).WriteJSON(proto.TError, failure)
	}
}

func (g *Init) receiveInputs(op *inputPhase, put *link.Stream) (err error) {
	reader := bufio.NewReader(put)
	summary, err := inputtransfer.ReadSummary(op.ctx, reader)
	if err != nil {
		return err
	}
	if summary.Nonce != op.control.Nonce || summary.Digest != op.control.Digest {
		return inputError("input summary names another reserved phase")
	}
	if err = summary.Validate(op.control.CopyMax, op.control.SeedMax); err != nil {
		return err
	}
	g.mu.Lock()
	op.bundleExpected = len(summary.Repositories) > 0
	g.mu.Unlock()
	prefix, err := inputtransfer.EncodeSummary(summary)
	if err != nil {
		return err
	}
	stage, err := g.newInputStage(op.control.Nonce)
	if err != nil {
		return err
	}
	defer func() {
		if stage != nil {
			err = errors.Join(err, stage.close())
		}
	}()
	footprint, err := checkInputStageSpace(op.ctx, stage.payload, summary)
	if err != nil {
		return err
	}
	g.mu.Lock()
	op.footprint = footprint
	g.mu.Unlock()
	control, err := inputtransfer.EncodeControl(op.control)
	if err != nil {
		return err
	}
	input := io.Reader(io.MultiReader(bytes.NewReader(control), bytes.NewReader(prefix), reader))
	if len(summary.Repositories) != 0 {
		input = io.MultiReader(input, &inputBundleReader{g: g, op: op})
	}
	executable, err := g.helperExecutable()
	if err != nil {
		return err
	}
	run := newOwnedCommand(op.ctx, proto.CtlExecArgs{Argv: []string{executable, inputtransfer.HelperMode}, Cwd: "/"}, inputCommandEnv())
	run.vmRole = vmSeedCommand
	var inputErr error
	code := g.executeOwned(run, ownedCommandIO{Out: put, Err: io.Discard, AbortOutput: func() { g.closeInputStreams(op) }, AttachInput: func(writer io.WriteCloser) func() {
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() { inputErr = errors.Join(inputErr, writer.Close()) }()
			_, inputErr = io.Copy(writer, input)
		}()
		return func() {
			select {
			case <-done:
				return
			default:
			}
			g.mu.Lock()
			streams := slices.Clone(op.streams)
			g.mu.Unlock()
			for _, stream := range streams {
				_ = stream.SetReadDeadline(time.Now())
			}
			<-done
		}
	}})
	run.cancel()
	if code != 0 || inputErr != nil || op.ctx.Err() != nil {
		return inputError("the owned input receiver did not finish successfully")
	}
	if err = inputtransfer.CloneStaged(op.ctx, stage.payload, op.control, summary, inputtransfer.CloneOperations{
		CloneRepository: func(ctx context.Context, index int, repository api.InputRepository, identity api.GitIdentity) error {
			return g.cloneInputRepository(ctx, stage, op.control.Session, index, repository, identity, op.control.SeedMax)
		},
	}); err != nil {
		return err
	}
	if err = g.applyInputIncludes(op.ctx, op.control, summary); err != nil {
		return err
	}
	completedStage := stage
	stage = nil
	if err = completedStage.close(); err != nil {
		return err
	}
	if err = op.ctx.Err(); err != nil {
		return err
	}
	if err = inputtransfer.Complete(op.ctx, put, op.control.Scope, summary.FileCount+len(summary.Repositories)); err != nil {
		return err
	}
	if err = put.Flush(op.ctx); err != nil {
		return err
	}
	if err = put.CloseWrite(); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inputs != op || g.ended {
		return inputError("the input owner changed before completion")
	}
	op.success = true
	return nil
}

type inputBundleReader struct {
	g      *Init
	op     *inputPhase
	stream *link.Stream
}

func (r *inputBundleReader) Read(data []byte) (int, error) {
	if r.stream == nil {
		r.g.mu.Lock()
		if r.g.inputs != r.op || r.g.ended || r.op.ctx.Err() != nil {
			r.g.mu.Unlock()
			return 0, inputError("the input bundle owner ended")
		}
		r.g.mu.Unlock()
		select {
		case r.stream = <-r.op.bundle:
		case <-r.op.ctx.Done():
			return 0, r.op.ctx.Err()
		case <-r.g.sess.Done():
			return 0, r.g.sess.Err()
		}
	}
	n, err := r.stream.Read(data)
	if err == io.EOF {
		_ = r.stream.CloseWrite()
	}
	return n, err
}

func (g *Init) closeInputStreams(op *inputPhase) {
	g.mu.Lock()
	streams := slices.Clone(op.streams)
	g.mu.Unlock()
	for _, stream := range streams {
		_ = stream.SetDeadline(time.Now())
		_ = stream.Close()
	}
}
func (g *Init) cancelInputs(scope inputtransfer.Scope) error {
	g.mu.Lock()
	op := g.inputs
	g.mu.Unlock()
	if op == nil || op.control.Scope != scope {
		return inputError("no matching input phase is owned")
	}
	op.cancel()
	g.closeInputStreams(op)
	<-op.done
	return nil
}
func (g *Init) stopInputs() {
	g.mu.Lock()
	op := g.inputs
	g.mu.Unlock()
	if op != nil {
		op.cancel()
		g.closeInputStreams(op)
		<-op.done
	}
}

// inputStartAllowedLocked distinguishes initial infrastructure preparation from
// a running member. Init.mu is held by the actual CtlStart reservation.
func (g *Init) inputStartAllowedLocked(a proto.CtlStartArgs) bool {
	if g.inputStart == nil {
		return !g.sessionPrepared && a.InputNonce == "" && a.InputDigest == ""
	}
	return g.inputs != nil && g.inputs.success && a.Session == g.inputStart.Session && a.InputNonce == string(g.inputs.control.Nonce) && a.InputDigest == g.inputs.control.Digest
}

func inputInfrastructure(a proto.CtlStartArgs) proto.CtlStartArgs {
	return proto.CtlStartArgs{PackageManagers: proto.ClonePackageManagersV1(a.PackageManagers), Decoys:proto.CloneDecoyFilesV1(a.Decoys),Session: a.Session, Agent: a.Agent, Env: a.Env, Cwd: a.Cwd, DNS: a.DNS, Proxy: a.Proxy, CACert: a.CACert, StateFiles: a.StateFiles, CaptureNonce: a.CaptureNonce, Restored: a.Restored, PendingMembers: a.PendingMembers}
}

func (g *Init) useInputPreparation(a *proto.CtlStartArgs) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inputStart == nil {
		if a.InputNonce != "" || a.InputDigest != "" || g.inputs != nil {
			return false, inputError("the member has no matching input reservation")
		}
		return false, nil
	}
	if !g.inputStartAllowedLocked(*a) || !reflect.DeepEqual(inputInfrastructure(*a), inputInfrastructure(*g.inputStart)) {
		return false, inputError("the start differs from its prepared input infrastructure")
	}
	a.Env = slices.Clone(g.inputEnv)
	return true, nil
}

func inputPhaseActive(op *inputPhase) bool {
	if op == nil {
		return false
	}
	select {
	case <-op.done:
		return false
	default:
		return true
	}
}
