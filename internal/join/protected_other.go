//go:build !linux

package join

import (
	"crypto/rand"
	"github.com/iamlakshya/simstim/internal/join/code"
	"os"
)

func randomName() string               { return rand.Text() }
func validAccount(s string) bool       { return code.ValidAccount(s) }
func fileOwner(os.FileInfo) (int, int) { return -1, -1 }
func protectedPath(string, bool) error {
	return codeFailure("join_platform", "Linux enrolment needs a Linux machine")
}
