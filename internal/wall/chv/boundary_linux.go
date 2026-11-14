//go:build linux

package chv

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
)

// CurrentMemoryBoundary checks the already-owned session unit. It creates or
// changes no unit, cgroup, kernel setting or namespace. The supervisor's unit
// owner must place this process within the admitted boundary before Start.
func CurrentMemoryBoundary(ctx context.Context, budget MemoryBudget) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return fmt.Errorf("read supervisor memory boundary: %w", err)
	}
	group := ""
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "0::"); ok {
			if group != "" {
				return fmt.Errorf("supervisor has ambiguous cgroup v2 membership")
			}
			group = value
		}
	}
	if !path.IsAbs(group) || path.Clean(group) != group || group == "/" || strings.ContainsRune(group, 0) {
		return fmt.Errorf("the supervisor has no owned cgroup v2 session boundary")
	}
	root, err := os.OpenRoot("/sys/fs/cgroup")
	if err != nil {
		return fmt.Errorf("open supervisor cgroup filesystem: %w", err)
	}
	defer root.Close()
	unit, err := root.OpenRoot(strings.TrimPrefix(group, "/"))
	if err != nil {
		return fmt.Errorf("pin supervisor memory boundary: %w", err)
	}
	defer unit.Close()
	return verifyMemoryBoundary(ctx, unit, budget)
}

func verifyMemoryBoundary(ctx context.Context, root *os.Root, budget MemoryBudget) error {
	for _, item := range []struct {
		name     string
		expected int64
	}{{"memory.max", budget.Limit}, {"memory.swap.max", 0}} {
		if err := ctx.Err(); err != nil {
			return err
		}
		file, err := root.Open(item.name)
		if err != nil {
			return fmt.Errorf("open session %s: %w", item.name, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(file, 65))
		file.Close()
		if readErr != nil {
			return fmt.Errorf("read session %s: %w", item.name, readErr)
		}
		value, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
		if len(data) > 64 || err != nil || value != item.expected {
			return fmt.Errorf("session %s is not the admitted microvm value %d", item.name, item.expected)
		}
	}
	return nil
}
