//go:build !linux

package supervisor

import "github.com/iamlakshya/simstim/internal/wall"

func processDiskResourcesV1(wall.Spec) (*diskResourcesV1, error) {
	return nil, diskFailure("disk_measure", "process sandbox storage requires the Linux host helper")
}
