package paths

import "path/filepath"

// ImagesDir is the host's persistent image store.
func ImagesDir() string { return filepath.Join(DataDir(), "images") }
