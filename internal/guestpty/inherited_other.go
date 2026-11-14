//go:build !linux

package guestpty

import (
	"fmt"
	"net"
)

// AdoptHolder requires the Linux microVM's actual init-owned credentials.
func AdoptHolder(string) (*Terminal, error) {
	return nil, fmt.Errorf("the init-owned VM terminal requires Linux")
}

// AdoptHook requires the Linux microVM's actual init-bound Unix listener.
func AdoptHook(string) (net.Listener, error) {
	return nil, fmt.Errorf("the init-owned VM hook listener requires Linux")
}
