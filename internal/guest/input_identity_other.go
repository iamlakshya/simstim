//go:build !linux

package guest

import "os"

func checkInputHelperAccount() error   { return inputError("the input receiver requires a Linux guest") }
func inputOwner(os.FileInfo, int) bool { return false }
