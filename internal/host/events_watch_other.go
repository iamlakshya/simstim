//go:build !linux

package host

import (
	"context"
	"fmt"
	"os"
	"time"
)

func lockEventsFile(string) func() { return func() {} }

func fileInode(os.FileInfo) uint64 { return 0 }

func watchEventsFile(ctx context.Context, _ string, onChange func() error) error {
	if err := onChange(); err != nil {
		return err
	}
	t := time.NewTicker(25 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("watch events stopped: %w", ctx.Err())
		case <-t.C:
			if err := onChange(); err != nil {
				return err
			}
		}
	}
}
