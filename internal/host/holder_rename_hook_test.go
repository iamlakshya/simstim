package host

import (
	"os"
	"path/filepath"
	"time"

	"github.com/iamlakshya/simstim/internal/paths"
)

// The holders of this package's tests are the test binary itself, started
// again, so these hooks are theirs too.
//
// A test stalls a holder's rename before it takes the session's lock: it
// makes the file RenameStall in the session's folder, and waits for
// RenameStalled. The rename goes on when RenameStall goes.
//
// A test stalls a new holder between its load of the manifest and its
// listen: it names a file in StartStallEnv, and makes it. The holder writes
// its session's ID to that file's name plus ".stalled", and goes on when
// the file goes, or after 15 s.
const (
	RenameStall   = "rename-stall"
	RenameStalled = "rename-stalled"
	StartStallEnv = "HOLDER_TEST_START_STALL"
)

func init() {
	beforeRenameLock = func(id string) {
		stall := filepath.Join(paths.SessionDir(id), RenameStall)
		if _, err := os.Stat(stall); err != nil {
			return
		}
		_ = os.WriteFile(filepath.Join(paths.SessionDir(id), RenameStalled), nil, 0o600)
		waitGoneFile(stall, 30*time.Second)
	}
	afterHolderLoad = func(id string) {
		stall := os.Getenv(StartStallEnv)
		if stall == "" {
			return
		}
		if _, err := os.Stat(stall); err != nil {
			return
		}
		_ = os.WriteFile(stall+".stalled", []byte(id), 0o600)
		waitGoneFile(stall, 15*time.Second)
	}
}

// waitGoneFile waits until the file p is gone, or for d.
func waitGoneFile(p string, d time.Duration) {
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(p); err != nil {
			return
		}
	}
}
