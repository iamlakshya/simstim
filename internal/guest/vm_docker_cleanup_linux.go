//go:build linux

package guest

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/jail"
)

func vmDockerMountTargets(snapshot string) ([]string, error) {
	points, err := jail.MountPoints(snapshot)
	if err != nil {
		return nil, err
	}
	var targets []string
	roots := []string{"/.persist/docker", filepath.Join("/run", brand.Name, "docker-exec")}
	for _, point := range points {
		for _, root := range roots {
			if strings.HasPrefix(point, root+"/") {
				targets = append(targets, point)
				break
			}
		}
	}
	// Stable order retains stacked mounts at the same point, one unmount each.
	sort.SliceStable(targets, func(i, j int) bool { return strings.Count(targets[i], "/") > strings.Count(targets[j], "/") })
	return targets, nil
}

func (v *vmRuntime) cleanupFiles() (result error) {
	v.waitFileOperations()
	defer func() {
		if result != nil {
			v.setError(result)
		}
	}()
	if v.memory == nil || !v.plan.Args.VM.Docker || !v.files.pivoted || v.files.back {
		return nil
	}
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return fmt.Errorf("read private Docker mounts: %w", err)
	}
	// This diagnostic cannot silently take the whole physical guest budget.
	// No RAM is reserved/subtracted from the agents' aggregate memory.max.
	bound := v.memory.total / 16
	data, readErr := io.ReadAll(io.LimitReader(file, bound+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return vmError("the guest Docker mount snapshot cannot be read")
	}
	if bound <= 0 || int64(len(data)) > bound {
		return vmError("the guest Docker mount snapshot exceeds its memory inspection budget")
	}
	targets, err := vmDockerMountTargets(string(data))
	if err != nil {
		return fmt.Errorf("decode private Docker mounts: %w", err)
	}
	for _, target := range targets {
		if err := v.files.ops.unmount(target, 0); err != nil {
			return fmt.Errorf("unmount private Docker data: %w", err)
		}
	}
	return nil
}
