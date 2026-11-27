//go:build windows

package cli

import (
	"fmt"
	"os"
	"testing"

	"github.com/iamlakshya/simstim/internal/testhome"
)

// Windows runs the portable CLI tests without linking the Unix host helper.
// Child scenes still finish their cleanups before their exit code is mapped.
func TestMain(m *testing.M) {
	done, err := testhome.Isolate()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	done()
	if code != 0 && os.Getenv(mainArgsEnv) != "" {
		code = childFailed
	}
	os.Exit(code)
}
