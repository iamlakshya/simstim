package guest

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/inputtransfer"
	"github.com/iamlakshya/simstim/internal/proto"
)

// RunInputApplyHelper is the closed fixed-account reviewed-include phase. It
// receives only the same trusted control/summary prefix after native clones.
// It neither invokes Git nor accepts a caller command, UID or source path.
func RunInputApplyHelper(args []string) (returned error) {
	if len(args) != 0 {
		return inputError("the input apply helper takes no arguments")
	}
	if err := checkInputHelperAccount(); err != nil {
		return err
	}
	ctx := context.Background()
	reader := bufio.NewReader(os.Stdin)
	control, err := inputtransfer.DecodeControl(reader)
	if err != nil {
		return err
	}
	summary, err := inputtransfer.ReadSummary(ctx, reader)
	if err != nil {
		return err
	}
	if _, err = proto.ReadLimit(reader, inputtransfer.FrameMax); !errors.Is(err, io.EOF) {
		return inputError("the input apply helper received trailing data")
	}
	stage, err := os.OpenRoot(filepath.Join(host.GuestRunDir(), "inputs", string(control.Nonce)))
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, stage.Close()) }()
	root, err := os.OpenRoot("/")
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, root.Close()) }()
	if err := inputtransfer.ApplyIncludes(ctx, inputtransfer.ReceiveOptions{Scope: control.Scope, Stage: stage, Root: root, CopyMax: control.CopyMax, SeedMax: control.SeedMax, Protected: control.Protected, BeforeCreate: files.CheckSpace}, summary); err != nil {
		return err
	}
	return inputtransfer.VerifyPrimaryPolicy(ctx, control, summary)
}
func (g *Init) applyInputIncludes(ctx context.Context, control inputtransfer.Control, summary api.InputSummary) error {
	if summary.FileCount == 0 && control.PrimaryRepository == "" {
		return nil
	}
	prefix, err := inputtransfer.EncodeControl(control)
	if err != nil {
		return err
	}
	metadata, err := inputtransfer.EncodeSummary(summary)
	if err != nil {
		return err
	}
	executable, err := g.helperExecutable()
	if err != nil {
		return err
	}
	run := newOwnedCommand(ctx, proto.CtlExecArgs{Argv: []string{executable, inputtransfer.HelperApplyMode}, Cwd: "/"}, inputCommandEnv())
	run.vmRole = vmSeedCommand
	defer run.cancel()
	code := g.executeOwned(run, ownedCommandIO{Input: io.MultiReader(bytes.NewReader(prefix), bytes.NewReader(metadata)), Out: io.Discard, Err: io.Discard})
	if code != 0 {
		return inputError("the fixed-account reviewed include application failed")
	}
	return ctx.Err()
}
