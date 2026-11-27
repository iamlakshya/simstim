//go:build !linux && !darwin

package host

func systemGit() (string, error) {
	return "", UserError("seed_git", "this host cannot run a Git seed jail", "Use a Linux host with the process wall.")
}
