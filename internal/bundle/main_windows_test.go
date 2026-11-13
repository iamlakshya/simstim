//go:build windows

package bundle_test

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/testhome"
)

// The tests run in a home of their own (testhome), as on Unix
// (main_unix_test.go), where the binary may also stand in for the helper.
func TestMain(m *testing.M) { testhome.Main(m) }
