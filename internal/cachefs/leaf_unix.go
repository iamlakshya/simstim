//go:build unix

package cachefs

import (
	"fmt"
	"path/filepath"
	"strings"
)

func safeNativeLeaf(name string) error {
	if name == "" || name == "." || name == ".." || filepath.IsAbs(name) || strings.ContainsAny(name, "/\x00") {
		return fmt.Errorf("entry must be one native leaf")
	}
	return nil
}
