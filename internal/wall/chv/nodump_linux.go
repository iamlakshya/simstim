//go:build linux

package chv

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/iamlakshya/simstim/internal/supply"
	"golang.org/x/sys/unix"
)

// executeOnlyVMM keeps the public pinned bytes unchanged. Linux's exec
// would_dump path applies suid_dumpable to an unreadable executable inode;
// the canonical jail drops all DAC capabilities before this external exec.
func executeOnlyVMM(ctx context.Context, item supply.Item, file *os.File) (*os.File, error) {
	if err := noDumpPrerequisite(); err != nil {
		return nil, err
	}
	return supply.SealRuntime(ctx, item, file)
}

func noDumpPrerequisite() error {
	data, err := os.ReadFile("/proc/sys/fs/suid_dumpable")
	if err != nil {
		return fmt.Errorf("read VMM NoDump execution invariant: %w", err)
	}
	return noDumpPolicy(data)
}

func noDumpPolicy(value []byte) error {
	if strings.TrimSpace(string(value)) != "0" {
		return fmt.Errorf("the host's fs.suid_dumpable policy cannot guarantee a non-dumpable execute-only VMM")
	}
	return nil
}

func helperNoDump() error {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return fmt.Errorf("clear VMM helper dumpability: %w", err)
	}
	return nil
}
