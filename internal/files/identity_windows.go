//go:build windows

package files

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// FILE_ID_INFO from WinBase, retaining all 128 bits needed by ReFS as well as
// NTFS. This is a native metadata layout, never a path-derived projection.
type windowsFileIDInfo struct {
	Volume uint64
	ID     [16]byte
}

var (
	_ [24 - unsafe.Sizeof(windowsFileIDInfo{})]byte
	_ [unsafe.Sizeof(windowsFileIDInfo{}) - 24]byte
	_ [8 - unsafe.Offsetof(windowsFileIDInfo{}.ID)]byte
	_ [unsafe.Offsetof(windowsFileIDInfo{}.ID) - 8]byte
)

// Identity returns the opened object's opaque native volume/full file identity.
// It never reopens by pathname or substitutes an identity after an API failure.
func Identity(file *os.File) (string, error) {
	if file == nil {
		return "", fmt.Errorf("the opened source file is unavailable")
	}
	var info windowsFileIDInfo
	if err := windows.GetFileInformationByHandleEx(windows.Handle(file.Fd()), windows.FileIdInfo,
		(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return "", fmt.Errorf("read the opened source file identity: %w", err)
	}
	return windowsFileIdentity(info), nil
}

func windowsFileIdentity(info windowsFileIDInfo) string {
	return fmt.Sprintf("win:%016x:%x", info.Volume, info.ID)
}
