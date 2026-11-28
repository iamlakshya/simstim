//go:build !unix

package client

import (
	"fmt"
	"os"
)

func openConfigNoBlock(path string) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return f, nil
}
