//go:build !unix

package supply

import (
	"fmt"
	"os"
)

func openReadOnly(root *os.Root, p string) (*os.File, error) {
	f, err := root.Open(p)
	if err != nil {
		return nil, fmt.Errorf("open %s read-only: %w", p, err)
	}
	return f, nil
}
