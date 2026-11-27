//go:build windows

package client

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// This exactly-two-link profile belongs only to the joined file's original
// private pair. It does not change cachefs' canonical single-link rules.
func joinedNativeIdentity(file *os.File, links uint64) (string, error) {
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	var native windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &native); err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || native.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || uint64(native.NumberOfLinks) != links || !joinedPrivateACL(file) {
		return "", inviteError("invite_ownership", "a joined file has foreign ACLs, reparse state or link count", "Keep the changed entry and inspect the original host removal.")
	}
	return fmt.Sprintf("windows:%d:%d:%d", native.VolumeSerialNumber, native.FileIndexHigh, native.FileIndexLow), nil
}
func joinedPrivateACL(file *os.File) bool {
	sd, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return false
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return false
	}
	admin, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.Equals(user.User.Sid) {
		return false
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return false
	}
	const writes = windows.GENERIC_WRITE | windows.GENERIC_ALL | windows.WRITE_DAC | windows.WRITE_OWNER | windows.DELETE | windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_ATTRIBUTES | windows.FILE_WRITE_EA | 0x40
	for n := uint32(0); n < uint32(dacl.AceCount); n++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(dacl, n, &ace) != nil || ace == nil {
			return false
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return false
		}
		if ace.Mask&writes == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() || !sid.Equals(user.User.Sid) && !sid.Equals(system) && !sid.Equals(admin) {
			return false
		}
	}
	return true
}
