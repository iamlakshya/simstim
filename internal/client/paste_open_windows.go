//go:build windows

package client

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// openResolved opens resolved, then asks Windows for the final path of the
// file that it opened, and checks that path's folders and file name. It
// also checks that resolved is not a link now, and is the file that it
// opened. Windows has no open relative to a folder handle in Go, so this is
// the closest match: a folder or a file that changed before the open, into
// a link or into a private one, shows in the final path or the identity,
// and the upload reads the file that was opened. A folder that changes
// again after the open, between the final path and the check of its
// folders, is not caught.
func openResolved(resolved string, folders []privateFolder) (*os.File, string) {
	f, err := os.Open(resolved)
	if err != nil {
		return nil, "could not be opened (" + err.Error() + ")"
	}
	why := checkOpened(f, resolved, folders)
	if why != "" {
		_ = f.Close()
		return nil, why
	}
	return f, ""
}

func checkOpened(f *os.File, resolved string, folders []privateFolder) string {
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return "changed while the client checked it: it is not a regular file"
	}
	// A leaf that became a link, or another reparse point, is refused.
	lst, err := os.Lstat(resolved)
	if err != nil || lst.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 || !os.SameFile(st, lst) {
		return "changed while the client checked it"
	}
	final, err := finalPath(f)
	if err != nil {
		return "could not be checked (" + err.Error() + ")"
	}
	if why := privateName(filepath.Base(final)); why != "" {
		return why
	}
	return inPrivateFolder(final, folders)
}

// finalPath is the path of an open file, with every link and junction
// resolved, without the \\?\ prefix.
func finalPath(f *os.File) (string, error) {
	buf := make([]uint16, 512)
	for {
		n, err := windows.GetFinalPathNameByHandle(windows.Handle(f.Fd()), &buf[0], uint32(len(buf)), 0)
		if err != nil {
			return "", err //nolint:wrapcheck // the caller names the file
		}
		if int(n) < len(buf) {
			p := windows.UTF16ToString(buf[:n])
			if rest, ok := strings.CutPrefix(p, `\\?\UNC\`); ok {
				return `\\` + rest, nil
			}
			return strings.TrimPrefix(p, `\\?\`), nil
		}
		buf = make([]uint16, n)
	}
}
