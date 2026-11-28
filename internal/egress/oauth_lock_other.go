//go:build !unix

package egress

func withAuthFileLock(_ string, fn func() error) error {
	return fn()
}
