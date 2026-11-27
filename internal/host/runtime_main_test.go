package host_test

import (
	"io"
	"os"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// The tests in this binary may start it again as a session's holder.
func TestMain(m *testing.M) {
	if os.Getenv(brand.Env("TEST_LEGACY_PID")) == "1" && len(os.Args) == 3 {
		ready := os.NewFile(3, "legacy process ready")
		_, _ = ready.Write([]byte{1})
		_ = ready.Close()
		wait := os.NewFile(4, "legacy process lifetime")
		_, _ = io.Copy(io.Discard, wait)
		_ = wait.Close()
		os.Exit(0)
	}
	hosttest.Main(m)
}
