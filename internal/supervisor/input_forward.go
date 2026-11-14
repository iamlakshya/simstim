package supervisor

import (
	"context"
	"errors"
	"os"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/inputtransfer"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/phaseclock"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/wall"
)

func (s *Supervisor) applyStartupInputs(ctx context.Context, args *proto.CtlStartArgs) (returned error) {
	input := s.startupInput
	if input == nil {
		if s.m.Inputs != nil && s.automatic == nil {
			return inputFailure("the declared input continuation is missing")
		}
		if s.m.Inputs != nil && s.m.Inputs.FileCount > 0 {
			return s.markCopiesPending(ctx, true)
		}
		return nil
	}
	return s.applyInputPhase(ctx, input, args)
}

func (s *Supervisor) applyInputPhase(ctx context.Context, input *startupInput, args *proto.CtlStartArgs) (returned error) {
	if input.runGeneration != s.generation || input.clock == nil {
		return inputFailure("the input owner belongs to another run")
	}
	defer input.clock.close()
	if err := input.clock.ctx.Err(); err != nil {
		return err
	}
	handshake, handshakeCancel := phaseclock.WithDeadline(ctx, input.clock.handshake)
	defer handshakeCancel()
	defer input.carrier.Close()
	stop := context.AfterFunc(ctx, func() { _ = input.carrier.Close() })
	defer stop()
	if err := s.requireInputGuestMode(handshake, args == nil); err != nil {
		return err
	}
	spool, err := os.CreateTemp(paths.SessionDir(s.id), ".input-declarations-")
	if err != nil {
		return err
	}
	receipt, err := newInputCompletion(spool)
	if err != nil {
		_ = spool.Close()
		_ = os.Remove(spool.Name())
		return err
	}
	input.completion = receipt
	defer func() {
		// A late operation with verified sender EOF retains exactly this
		// owner for idempotent Finish recovery, including a new CLI process.
		if args == nil && receipt.canRecover() {
			return
		}
		returned = errors.Join(returned, receipt.cleanup())
	}()
	put, err := input.carrier.Accept(handshake)
	if err != nil {
		return err
	}
	if put.Kind() != link.KindPut || !inputHeader(put, input.scope) {
		_ = put.Close()
		return inputFailure("the first input stream is not its reserved put")
	}
	defer put.Close()
	protected, err := s.protectedInputPaths()
	if err != nil {
		return err
	}
	control := inputtransfer.Control{Scope: input.scope, CopyMax: input.copyMax, SeedMax: input.seedMax, Protected: protected}
	var prepared *proto.CtlStartArgs
	if args != nil {
		copy := *args
		copy.Argv, copy.Hooks, copy.Secrets, copy.SecretModes, copy.Missing = nil, nil, nil, nil, nil
		copy.RunToken, copy.Shell = "", ""
		copy.DefaultShell = false
		prepared = &copy
	}
	request := s.ctl.request
	if args == nil {
		request = s.ctl.requestDetached
	}
	receipt.mu.Lock()
	receipt.startAttempted = true
	receipt.mu.Unlock()
	defer func() {
		if returned != nil {
			cancel, stop := phaseclock.WithDeadline(s.lifetimeContext(), now().Add(clientWriteWait))
			defer stop()
			if args == nil {
				_ = s.ctl.requestDetached(cancel, inputtransfer.CtlCancel, input.scope, nil)
			} else {
				_ = s.ctl.request(cancel, inputtransfer.CtlCancel, input.scope, nil)
			}
		}
	}()
	if err := request(handshake, inputtransfer.CtlPrepare, inputtransfer.PrepareArgs{Control: control, Start: prepared}, nil); err != nil {
		return err
	}
	forward := func(phase context.Context) error {
		stopPhase := context.AfterFunc(phase, func() { input.clock.cancel(context.Canceled) })
		defer stopPhase()
		return inputtransfer.Forward(input.clock.ctx, put, inputtransfer.ForwardOptions{Scope: input.scope, Summary: input.summary,
			CopyMax: control.CopyMax, SeedMax: control.SeedMax, Protected: control.Protected, Spool: spool,
			OpenGuest: func(ctx context.Context) (inputtransfer.Stream, error) {
				return s.link.Open(ctx, link.KindPut, inputtransfer.Header{Purpose: "inputs", Scope: input.scope})
			},
			Progress: func(bytes int64) {
				s.mu.Lock()
				valid := s.generation == input.runGeneration
				s.mu.Unlock()
				if valid {
					input.clock.progress(bytes)
				}
			},
			SenderEOF: func() { receipt.markSenderEOF(); input.clock.senderEOF() },
			OpenClientBundle: func(ctx context.Context, header inputtransfer.Header) (inputtransfer.Stream, error) {
				stream, err := input.carrier.Accept(ctx)
				if err != nil {
					return nil, err
				}
				if stream.Kind() != link.KindBundle || !inputHeader(stream, header.Scope) || !put.PeerWriteClosed() {
					_ = stream.Close()
					return nil, inputFailure("invalid or early input bundle continuation")
				}
				return stream, nil
			},
			OpenGuestBundle: func(ctx context.Context, header inputtransfer.Header) (inputtransfer.Stream, error) {
				return s.link.Open(ctx, link.KindBundle, header)
			},
			Finish: func(ctx context.Context) error { return s.finishInput(ctx, input, true) },
			Audit: func(file api.InputFile) error {
				kind := "copy_in"
				if file.Include {
					kind = "include_exposure"
				}
				s.audit.write(kind, map[string]any{"guest": file.Path, "bytes": file.Size, "directory": file.Directory})
				return nil
			},
		})
	}
	if budget, ok := ctx.(*activeBootBudget); ok {
		err = s.separatePhase(budget, input.clock.absolute, forward)
	} else {
		err = forward(ctx)
	}
	if err != nil {
		return err
	}
	if input.runGeneration != s.generation {
		return inputFailure("the run changed before input completion")
	}
	if args != nil {
		args.InputNonce, args.InputDigest = string(input.scope.Nonce), input.scope.Digest
	}
	return nil
}

func inputHeader(stream *link.Stream, scope inputtransfer.Scope) bool {
	if !api.ExactJSONFields(stream.HeaderBytes(), struct {
		Kind link.Kind `json:"kind"`
		inputtransfer.Header
	}{}) {
		return false
	}
	var header inputtransfer.Header
	return stream.Header(&header) == nil && header.Purpose == "inputs" && header.Scope == scope
}

func (s *Supervisor) protectedInputPaths() ([]string, error) {
	s.mu.Lock()
	m := host.CloneManifest(*s.m)
	s.mu.Unlock()
	protected := []string{"/opt/agents", "/.tools", "/.persist"}
	if m.Files != nil {
		for _, mount := range slices.Concat(m.Files.Mounts, m.Files.Installs) {
			protected = append(protected, mount.Guest)
		}
	}
	state, err := host.SandboxStatePlan(&m)
	if err != nil {
		return nil, err
	}
	statePaths, err := state.ProtectedPaths(host.GuestHome)
	if err != nil {
		return nil, err
	}
	protected = append(protected, statePaths...)
	if spec, present := wall.ExpectedSpec(s.lifetimeContext()); present {
		paths, err := (files.StatePlan{Dirs: spec.StateDirs, Files: spec.StateFiles}).ProtectedPaths(host.GuestHome)
		if err != nil {
			return nil, err
		}
		protected = append(protected, paths...)
	}
	if s.vmPlan != nil {
		paths, err := s.vmPlan.State.ProtectedPaths(host.GuestHome)
		if err != nil {
			return nil, err
		}
		protected = append(protected, paths...)
	}
	if err := inputtransfer.ValidateProtected(protected); err != nil {
		return nil, err
	}
	return protected, nil
}

func (s *Supervisor) requireInputGuest(ctx context.Context) error {
	return s.requireInputGuestMode(ctx, false)
}
func (s *Supervisor) requireInputGuestMode(ctx context.Context, detached bool) error {
	var result proto.CtlGuestFeatures
	request := s.ctl.request
	if detached {
		request = s.ctl.requestDetached
	}
	if err := request(ctx, proto.CtlGuestCapabilities, struct{}{}, &result); err != nil {
		return err
	}
	if result.Version != 1 || !slices.Contains(result.Features, inputtransfer.Feature) {
		return inputFailure("the actual guest cannot apply the complete declared inputs")
	}
	return nil
}

// commitInputCompletion publishes only this exact current-run/scope success,
// after owned metadata and guest resources ended and before client completion.
func (s *Supervisor) commitInputCompletion(ctx context.Context, input *startupInput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	var snapshot host.Manifest
	return s.writeManifestChange(&snapshot, false, nil, nil, func(m *host.Manifest) error {
		if input.runGeneration != s.generation || m.Inputs == nil || !m.Inputs.Equal(input.summary) || input.scope.Session != s.id || input.scope.Nonce != m.Inputs.Nonce || input.scope.Digest != m.Inputs.Digest {
			return inputFailure("the exact pending input scope changed before completion")
		}
		m.CopiesPending = false
		return nil
	}, func(m *host.Manifest) { s.m.CopiesPending = m.CopiesPending })
}
