//go:build !unix

package image

import "sync"

var fallbackLocks sync.Map

func lockFile(path string, _ bool) (func(), error) {
	v, _ := fallbackLocks.LoadOrStore(path, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock, nil
}
