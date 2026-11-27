//go:build linux

package host

import "os"

func mappedExecutable() (string, os.FileInfo, error) {
	name, err := os.Executable()
	if err != nil {
		return "", nil, placementError("cannot identify the running helper executable")
	}
	mapped, err := os.Open("/proc/self/exe")
	if err != nil {
		return "", nil, placementError("cannot anchor the running helper executable")
	}
	defer mapped.Close()
	info, err := mapped.Stat()
	if err != nil {
		return "", nil, placementError("cannot stat the running helper executable")
	}
	current, err := mappedExecutableMatches(name, info)
	return name, current, err
}
