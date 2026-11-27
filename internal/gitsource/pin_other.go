//go:build !linux && !darwin

package gitsource

import "os"

func (a sourceAuthority) pinMetadataDirectory(name string) (*os.File, error) {
	pin, err := a.pinTrustedSource(name, true)
	if err != nil {
		return nil, err
	}
	defer pin.close()
	return pin.roots[len(pin.roots)-1].Open(".")
}
