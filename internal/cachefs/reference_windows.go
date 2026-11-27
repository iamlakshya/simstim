//go:build windows

package cachefs

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func referenceIdentity(file *os.File, _ os.FileInfo) (string, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return "", fmt.Errorf("read native file identity: %w", err)
	}
	sd, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return "", fmt.Errorf("read native file owner identity: %w", err)
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return "", fmt.Errorf("native file owner identity is unavailable")
	}
	return fmt.Sprintf("windows:%08x:%08x%08x:%08x%08x:%s", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow, info.CreationTime.HighDateTime, info.CreationTime.LowDateTime, owner.String()), nil
}
