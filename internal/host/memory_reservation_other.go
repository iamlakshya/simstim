//go:build !linux

package host

import "context"

func readMemoryUnitV1(context.Context, string, bool) (memoryUnitFactsV1, error) {
	return memoryUnitFactsV1{}, UserError("wall_unavailable", "native sandbox memory admission requires a Linux host", "Use a Linux host for this sandbox.")
}

func memoryUnitIDsV1(context.Context) ([]string, error) {
	return nil, UserError("no_room", "native canonical cgroup memory accounting is unavailable on this platform", "Use a supported Linux host with cgroup v2 for sandbox admission.")
}

func readAuxMemoryUnitV1(context.Context, string) (memoryUnitFactsV1, error) {
	return memoryUnitFactsV1{}, UserError("no_room", "native auxiliary unit accounting requires a Linux cgroup v2 host", "Use the original supported Linux host and preserve the auxiliary operation.")
}
