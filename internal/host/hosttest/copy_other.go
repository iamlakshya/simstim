//go:build !darwin

package hosttest

func cloneOrCopy(src, dst string) error {
	return copyFileBytes(src, dst)
}
