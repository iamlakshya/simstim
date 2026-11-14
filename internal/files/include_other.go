//go:build !linux && !darwin

package files

import "os"

func includedFlags() int { return os.O_RDONLY }
