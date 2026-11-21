package supervisor

import (
	"os"
)

// orDefault is n, or def when n is not positive.
func orDefault(n, def int) int {
	if n <= 0 {
		return def
	}
	return n
}

// homeDir is the user's home, where alert commands run.
func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}
