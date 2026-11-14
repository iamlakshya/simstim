//go:build windows

package files

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// SpaceAt obtains the volume path from the actual pinned directory handle.
func SpaceAt(root *os.Root) (Space, error) {
	if root == nil {
		return Space{}, fmt.Errorf("filesystem root is missing")
	}
	file, err := root.Open(".")
	if err != nil {
		return Space{}, err
	}
	defer file.Close()
	buffer := make([]uint16, 512)
	for {
		n, err := windows.GetFinalPathNameByHandle(windows.Handle(file.Fd()), &buffer[0], uint32(len(buffer)), 0)
		if err != nil {
			return Space{}, err
		}
		if n == 0 || n > 64<<10 {
			return Space{}, fmt.Errorf("filesystem handle has an invalid final path")
		}
		if int(n) >= len(buffer) {
			buffer = make([]uint16, n+1)
			continue
		}
		var space Space
		if err = windows.GetDiskFreeSpaceEx(&buffer[0], &space.Available, &space.Total, nil); err != nil {
			return Space{}, err
		}
		return space, nil
	}
}
