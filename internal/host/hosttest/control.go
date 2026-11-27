package hosttest

import (
	"fmt"
	"runtime"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/testhome"
)

// configureControls owns a separate root because canonical test homes cannot
// fit the unchanged epoch, route and OpenSSH temporary bind suffix together.
// Production selection and all of its guards remain unchanged.
func configureControls() (func() error, error) {
	if runtime.GOOS == "windows" {
		return func() error { return nil }, nil
	}
	dir, err := testhome.NewSocketDir(15)
	if err != nil {
		return nil, err
	}
	restore, err := client.ConfigureControlFallbackForTesting(dir.Path())
	if err != nil {
		_ = dir.Close()
		return nil, err
	}
	unregister := dir.Activate()
	return func() error {
		restore()
		unregister()
		if err := dir.Close(); err != nil {
			return fmt.Errorf("close the test control root: %w", err)
		}
		return nil
	}, nil
}
