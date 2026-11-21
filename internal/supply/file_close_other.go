//go:build !linux

package supply

import (
	"fmt"
	"os"
)

// Non-Linux callers retain ordinary successful concrete Close. No nonnil
// errno acquires the Linux late-close inference on another kernel.
func CloseFileOnceV1(file *os.File) FileCloseFactsV1 {
	if file == nil {
		return FileCloseFactsV1{Unknown: fmt.Errorf("the actual file close has no original object")}
	}
	err := file.Close()
	if err == nil {
		return FileCloseFactsV1{Retired: true}
	}
	return FileCloseFactsV1{Diagnostic: err, Unknown: err}
}
func CheckFileCloseThreadV1() error {
	return fmt.Errorf("native first-close profile observation requires Linux")
}
