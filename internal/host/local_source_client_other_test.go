//go:build !linux

package host_test

import (
	"fmt"

	"github.com/iamlakshya/simstim/internal/sourcecompose"
)

func init() {
	if err := sourcecompose.Install(); err != nil {
		panic(fmt.Sprintf("install canonical host-test source provider: %v", err))
	}
}
