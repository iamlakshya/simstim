//go:build !race && (linux || darwin)

package client

import "slices"

func localGitHelperEnv(env []string) []string {
	return slices.Clone(env)
}
