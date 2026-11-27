//go:build !linux

package process

import (
	"fmt"
	"os"
	"runtime"
)

func localSystem() probeSystem {
	return probeSystem{os: runtime.GOOS, readFile: os.ReadFile}
}

// ProbeChild is unavailable on hosts without Linux user namespaces.
func ProbeChild() error {
	return fmt.Errorf("process probe child needs Linux")
}
