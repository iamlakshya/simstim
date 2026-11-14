package guest

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/inputtransfer"
)

// RunInputHelper is the closed fixed-account receiver mode. Init supplies its
// trusted control prefix on stdin, then only the declared input domain. It
// accepts no command or filesystem path, and never executes Git or an agent.
func RunInputHelper(args []string) (returned error) {
	if len(args) != 0 {
		return inputError("the input receiver takes no arguments")
	}
	if err := checkInputHelperAccount(); err != nil {
		return err
	}
	reader := bufio.NewReader(os.Stdin)
	control, err := inputtransfer.DecodeControl(reader)
	if err != nil {
		return err
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
	_, err = inputtransfer.Receive(context.Background(), reader, os.Stdout, inputtransfer.ReceiveOptions{
		Scope: control.Scope, Stage: stage, Root: root, CopyMax: control.CopyMax, SeedMax: control.SeedMax, Protected: control.Protected, BeforeCreate: files.CheckSpace,
	})
	return err
}
