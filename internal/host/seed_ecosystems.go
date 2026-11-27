package host

import "github.com/iamlakshya/simstim/internal/seedmeta"

// seedEcosystems uses createSeed's original jail-safe Git closure against
// the exact committed staging tree. It never opens a working-tree file.
func seedEcosystems(run func(string, ...string) (string, error), staging, commit string) ([]string, error) {
	if !seedCommitRE.MatchString(commit) {
		return nil, UserError("seed_metadata", "seed ecosystem facts have no selected commit", "Prepare the original committed seed again.")
	}
	args := append([]string{"ls-tree", "-z", "--name-only", commit, "--"}, seedmeta.RootManifests()...)
	listing, err := run(staging, args...)
	if err != nil {
		return nil, err
	}
	return seedmeta.Ecosystems([]byte(listing))
}
