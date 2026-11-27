//go:build linux

package supervisor

import (
	"github.com/iamlakshya/simstim/internal/proc"
	"os"
)

func noneSelfBirthV1() (string, error) {
	raw, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return "", err
	}
	return proc.LinuxBirthTicksV1(raw)
}
