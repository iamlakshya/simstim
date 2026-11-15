//go:build windows

package files

import (
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"strings"
	"unsafe"
)

// openRegularFile uses root-relative native handles and never reparses a path
// component. It is the same no-follow regular-file operation as Unix openat.
func openRegularFile(root *os.Root, name string) (*os.File, error) {
	return openRegularAccess(root, name, false)
}
func openRegularWriteFile(root *os.Root, name string) (*os.File, error) {
	return openRegularAccess(root, name, true)
}
func openRegularAccess(root *os.Root, name string, writable bool) (*os.File, error) {
	current, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	parts := strings.Split(name, "/")
	for index, part := range parts {
		object, err := windows.NewNTUnicodeString(part)
		if err != nil {
			current.Close()
			return nil, fmt.Errorf("encode rooted file component: %w", err)
		}
		attrs := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(current.Fd()), ObjectName: object, Attributes: windows.OBJ_DONT_REPARSE | windows.OBJ_CASE_INSENSITIVE}
		attrs.Length = uint32(unsafe.Sizeof(attrs))
		options := uint32(windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT)
		if index < len(parts)-1 {
			options |= windows.FILE_DIRECTORY_FILE
		} else {
			options |= windows.FILE_NON_DIRECTORY_FILE
		}
		var next windows.Handle
		var status windows.IO_STATUS_BLOCK
		access := uint32(windows.FILE_GENERIC_READ)
		if writable && index == len(parts)-1 {
			access |= windows.FILE_GENERIC_WRITE
		}
		err = windows.NtCreateFile(&next, access, &attrs, &status, nil, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN, options, 0, 0)
		closeErr := current.Close()
		if err != nil {
			var native windows.NTStatus
			if errors.As(err, &native) {
				err = native.Errno()
			}
			return nil, errors.Join(fmt.Errorf("open rooted file without reparse: %w", err), closeErr)
		}
		if closeErr != nil {
			windows.CloseHandle(next)
			return nil, closeErr
		}
		current = os.NewFile(uintptr(next), "rooted-regular-file")
		var info windows.ByHandleFileInformation
		if err = windows.GetFileInformationByHandle(next, &info); err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			current.Close()
			return nil, fmt.Errorf("rooted file component is not an ordinary object")
		}
	}
	return current, nil
}
