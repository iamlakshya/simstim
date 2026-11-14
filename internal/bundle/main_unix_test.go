//go:build !windows

package bundle_test

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// The tests in this binary may start it again as a session's holder, and
// they run in a home of their own (hosttest.Main, testhome). Windows has
// its own TestMain (main_windows_test.go).
func TestMain(m *testing.M) { hosttest.Main(m) }
