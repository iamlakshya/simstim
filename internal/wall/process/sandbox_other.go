//go:build !linux

package process

import "github.com/iamlakshya/simstim/internal/wall"

func newSandbox(wall.Spec) (wall.Sandbox, error) {
	return nil, setupError("the process wall needs Linux", nil)
}
