//go:build !linux

package runtimecache

// Non-Linux fixtures retain their existing publication implementation. The
// original incoming root still owns cleanup; native Linux never takes this path.
func linkRuntimePartV1(part *Part, target string) error {
	directory, err := part.store.root.Open("sha256")
	if err != nil {
		return err
	}
	part.publicationDir = directory
	err = part.store.root.Link(part.name, target)
	part.linked = err == nil
	return err
}
