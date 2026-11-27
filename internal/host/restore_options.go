package host

import (
	"slices"
)

// withOptions returns argv with the arguments of opts added: before the
// first "--", which ends the agent's options, or else at the end.
func withOptions(argv []string, opts ...[]string) []string {
	extra := slices.Concat(opts...)
	if i := slices.Index(argv, "--"); i >= 0 {
		return slices.Concat(argv[:i], extra, argv[i:])
	}
	return slices.Concat(argv, extra)
}

func withLabel(l map[string]string, k, v string) map[string]string {
	if l == nil {
		l = map[string]string{}
	}
	l[k] = v
	return l
}
