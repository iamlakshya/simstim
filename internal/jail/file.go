package jail

import "os"

// fileOpen checks the File and its underlying handle without File.Fd,
// which can switch a pollable descriptor into blocking mode.
func fileOpen(file *os.File) bool {
	if file == nil {
		return false
	}
	raw, err := file.SyscallConn()
	if err != nil {
		return false
	}
	if err := raw.Control(func(uintptr) {}); err != nil {
		return false
	}
	_, err = file.Stat()
	return err == nil
}
