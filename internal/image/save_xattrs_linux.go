//go:build linux

package image

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func savedFileXattrs(file *os.File, header *tar.Header) error {
	// Linux's xattr value and complete name-list limits are both 64 KiB.
	// https://man7.org/linux/man-pages/man7/xattr.7.html
	const maximum = 64 << 10
	names := make([]byte, maximum)
	count, err := unix.Flistxattr(int(file.Fd()), names)
	if errors.Is(err, unix.ENOTSUP) {
		return nil
	}
	if err != nil {
		return savedRootChanged()
	}
	for _, raw := range bytes.Split(names[:count], []byte{0}) {
		name := string(raw)
		if name == "" || strings.HasPrefix(name, "trusted.") {
			continue
		}
		value := make([]byte, maximum)
		count, err := unix.Fgetxattr(int(file.Fd()), name, value)
		if err != nil {
			return savedRootChanged()
		}
		if header.PAXRecords == nil {
			header.PAXRecords = make(map[string]string)
		}
		header.PAXRecords["SCHILY.xattr."+name] = string(value[:count])
	}
	return nil
}
