//go:build !linux

package host

import "fmt"

func imageWorkerConfinement() error {
	return fmt.Errorf("image conversion needs a Linux converter jail. Try: use a Linux host")
}
