package paths

import "path/filepath"

// ArtefactsDir is the host's persistent verified runtime store.
func ArtefactsDir() string { return filepath.Join(DataDir(), "artefacts") }
