//go:build linux

package process

import "github.com/iamlakshya/simstim/internal/wall"

func init() {
	// Registration follows the complete Prepare/Open/Start implementation. The
	// probe still requires the actual static launcher and a real smoke result.
	if err := wall.Register(Driver{}); err != nil {
		panic(err)
	}
}
