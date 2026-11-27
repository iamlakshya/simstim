package hosttest

import (
	"io"
	"os"
)

// linkOrCopy makes dst the same file as src: a hard link when it can, so
// that the copy is instant, or else a copy. Never a symbolic link: Linux
// names a process's executable by the link's target.
func linkOrCopy(src, dst string) error {
	_ = os.Remove(dst)
	if os.Link(src, dst) == nil {
		return nil
	}
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
