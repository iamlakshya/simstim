package host

import "os"

func mappedExecutableMatches(name string, before os.FileInfo) (os.FileInfo, error) {
	current, err := os.Lstat(name)
	if err != nil || before == nil || !os.SameFile(before, current) || !current.Mode().IsRegular() || current.Mode().Perm()&0o022 != 0 {
		return nil, placementError("the running helper is unlinked, replaced or unsafe")
	}
	_, _, uid, links, ok := footprintIdentity(current)
	if !ok || uid != uint64(os.Getuid()) || links != 1 {
		return nil, placementError("the helper executable has uncertain ownership or another link")
	}
	return current, nil
}
