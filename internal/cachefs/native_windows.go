//go:build windows

package cachefs

import (
	"errors"
	"io/fs"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// FileInfo carries no owner SID on Windows. Every trust decision also checks
// the opened descriptor through descriptorSafe, including pruning and locks.
func owned(_ fs.FileInfo) bool { return true }
func safeDirectory(root *os.Root, name string, st fs.FileInfo, private bool) bool {
	if st == nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return false
	}
	f, err := root.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !os.SameFile(st, after) {
		return false
	}
	var native windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &native) != nil || native.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return false
	}
	return secureACL(f, private)
}
func privateMode(_ fs.FileInfo) bool { return true } // The descriptor ACL supplies the access check.
func modeMatches(st fs.FileInfo, mode fs.FileMode) bool {
	if mode == 0o444 || mode == 0o555 {
		return st.Mode().Perm()&0o222 == 0
	}
	return st.Mode().Perm()&0o200 != 0
}
func descriptorSafe(f *os.File, st fs.FileInfo) bool {
	var info windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info) != nil || info.NumberOfLinks != 1 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return false
	}
	return st.Mode().IsRegular() && secureACL(f, true)
}
func secureACL(f *os.File, private bool) bool {
	sd, err := windows.GetSecurityInfo(windows.Handle(f.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
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
	if err != nil || owner == nil || (!owner.Equals(user.User.Sid) && (private || (!owner.Equals(system) && !owner.Equals(admin)))) {
		return false
	}
	if !private {
		return true
	} // Only the final anchored subtree is an owned private cache.
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return false
	}
	const writeMask = windows.GENERIC_WRITE | windows.GENERIC_ALL | windows.WRITE_DAC | windows.WRITE_OWNER | windows.DELETE | windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_ATTRIBUTES | windows.FILE_WRITE_EA | 0x40
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(dacl, i, &ace) != nil || ace == nil {
			return false
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return false
		}
		if ace.Mask&writeMask == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() || (!sid.Equals(user.User.Sid) && !sid.Equals(system) && !sid.Equals(admin)) {
			return false
		}
	}
	return true
}
func openNoFollow(root *os.Root, name string, flags int, mode fs.FileMode) (*os.File, error) {
	return root.OpenFile(name, flags, mode)
}
func tryLock(f *os.File, mode LockMode) (bool, error) {
	var o windows.Overlapped
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY)
	if mode == Shared {
		flags = windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, &o)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return err == nil, err
}
func syncDirectory(_ *os.File) error { return nil } // Windows does not expose directory flush through os.File.
