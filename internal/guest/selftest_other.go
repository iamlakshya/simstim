//go:build !linux

package guest

import "errors"

var errLinuxOnly = errors.New("microvm guest self-test runs on Linux only")

func prepareSelfTestFS() error                 { return errLinuxOnly }
func statRootDev() (uint32, uint32, error)     { return 0, 0, errLinuxOnly }
func statDM0BlockDev() (uint32, uint32, error) { return 0, 0, errLinuxOnly }
func powerOffGuest() error                     { return errLinuxOnly }
