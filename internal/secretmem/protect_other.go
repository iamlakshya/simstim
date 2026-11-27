//go:build !unix

package secretmem

import "errors"

func Protect() error {
	return errors.New("this platform has no supported trusted-process memory guard")
}

func Inspect() (State, error) {
	return State{}, errors.New("this platform cannot inspect trusted-process memory guards")
}
