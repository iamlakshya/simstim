package host

import "github.com/iamlakshya/simstim/internal/gitsource"

func seedRoot(dir string) (string, []string, error) {
	return gitsource.Metadata(gitContext(), dir, hostSourceInventory)
}
