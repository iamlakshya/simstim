//go:build windows

package cachefs

import (
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// nativeRenameInformation is the documented variable-length NT class10
// layout. Replace stays zero, so an existing destination is never replaced.
type nativeRenameInformation struct {
	Replace    uint8
	Directory  windows.Handle
	NameLength uint32
	Name       [1]uint16
}

func renameBetweenNoReplaceNative(root *os.Root, from string, target *os.Root, to string) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	name, err := windows.NewNTUnicodeString(from)
	if err != nil {
		return err
	}
	attributes := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(directory.Fd()), ObjectName: name, Attributes: windows.OBJ_CASE_INSENSITIVE}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var source windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&source, windows.DELETE|windows.SYNCHRONIZE|windows.FILE_READ_ATTRIBUTES, &attributes, &status, nil, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN, windows.FILE_OPEN_REPARSE_POINT|windows.FILE_OPEN_FOR_BACKUP_INTENT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	runtime.KeepAlive(name)
	if err != nil {
		return fmt.Errorf("open anchored rename source: %w", renameNativeError(err))
	}
	defer windows.CloseHandle(source)
	toName, err := windows.UTF16FromString(to)
	if err != nil {
		return err
	}
	if _, err := windows.NewNTUnicodeString(to); err != nil {
		return err
	}
	offset := unsafe.Offsetof(nativeRenameInformation{}.Name)
	buffer := make([]byte, int(offset)+2*len(toName))
	information := (*nativeRenameInformation)(unsafe.Pointer(&buffer[0]))
	destination, err := target.Open(".")
	if err != nil {
		return err
	}
	defer destination.Close()
	information.Directory = windows.Handle(destination.Fd())
	information.NameLength = uint32(2 * (len(toName) - 1))
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(&buffer[offset])), len(toName)), toName)
	err = windows.NtSetInformationFile(source, &status, &buffer[0], uint32(len(buffer)), windows.FileRenameInformation)
	runtime.KeepAlive(toName)
	runtime.KeepAlive(buffer)
	if err != nil {
		return fmt.Errorf("rename anchored leaf without replacement: %w", renameNativeError(err))
	}
	return nil
}

func renameNativeError(err error) error {
	if status, ok := err.(windows.NTStatus); ok {
		return status.Errno()
	}
	return err
}
