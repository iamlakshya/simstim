//go:build linux

package chv

import (
	"fmt"
	"os"
)

// These are the canonical jail's inner IDs, not host passwd/group guesses.
// Checking inside both helpers prevents a lost subuid mapping from silently
// changing the persistent disk's identity mode before format or VMM exec.
func helperUID(mode string) error { return checkHelperUID(mode, os.Getuid()) }

func checkHelperUID(mode string, uid int) error {
	if mode == "login" && uid == 0 || mode == "subuid" && uid == 1 {
		return nil
	}
	return fmt.Errorf("the microvm helper's actual jail identity differs from its recorded mode")
}
