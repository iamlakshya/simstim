package hosttest

import (
	"io"
	"os"
)

// copyFile writes a standalone copy of src at dst. It never uses a symlink
// (Linux names a process's executable by the link's target) and never hard-links
// src (the host helper checks that its running executable has a single link).
func copyFile(src, dst string) error {
	return cloneOrCopy(src, dst)
}

func copyFileBytes(src, dst string) error {
	_ = os.Remove(dst)
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
