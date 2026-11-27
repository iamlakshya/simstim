package image

import (
	"path"
	"slices"
	"strings"
	"unicode/utf8"
)

func agentsHostPath(name string) bool {
	return utf8.ValidString(name) && name != "/" && path.IsAbs(name) && path.Clean(name) == name && !strings.ContainsAny(name, "\x00\r\n")
}
func agentsInputPlan(inputs []AgentsInputV1) ([]AgentsInputV1, error) {
	plan := slices.Clone(inputs)
	slices.SortFunc(plan, func(a, b AgentsInputV1) int { return strings.Compare(a.HostPath, b.HostPath) })
	var kept []AgentsInputV1
	for _, source := range plan {
		kinds := 0
		if source.Folder != nil {
			kinds++
		}
		if source.File != nil {
			kinds++
		}
		if source.LinkInfo != nil {
			kinds++
		}
		if !agentsHostPath(source.HostPath) || kinds != 1 {
			return nil, agentsDiskError("the installed source plan has an invalid path or capability")
		}
		covered := false
		for _, parent := range kept {
			if source.HostPath == parent.HostPath {
				return nil, agentsDiskError("the installed source plan repeats a path")
			}
			if strings.HasPrefix(source.HostPath, parent.HostPath+"/") {
				if parent.Folder == nil {
					return nil, agentsDiskError("an installed source is below a non-directory")
				}
				covered = true
				break
			}
		}
		if !covered {
			kept = append(kept, source)
		}
	}
	return kept, nil
}
func agentsBinaryPlan(binaries []AgentBinaryV1, inputs []AgentsInputV1) ([]AgentBinaryV1, error) {
	plan := slices.Clone(binaries)
	slices.SortFunc(plan, func(a, b AgentBinaryV1) int { return strings.Compare(a.Name, b.Name) })
	for i, binary := range plan {
		if binary.Name == "" || binary.Name == "." || binary.Name == ".." || path.Base(binary.Name) != binary.Name || !utf8.ValidString(binary.Name) || strings.ContainsAny(binary.Name, "\x00\r\n") || !agentsHostPath(binary.ResolvedPath) {
			return nil, agentsDiskError("an installed binary has an invalid name or resolved path")
		}
		if i > 0 && plan[i-1].Name == binary.Name {
			return nil, agentsDiskError("two installed binaries select the same bin entry")
		}
		found := false
		for _, source := range inputs {
			if binary.ResolvedPath == source.HostPath && source.File != nil || source.Folder != nil && strings.HasPrefix(binary.ResolvedPath, source.HostPath+"/") {
				found = true
				break
			}
		}
		if !found {
			return nil, agentsDiskError("a resolved binary has no captured installed source")
		}
	}
	return plan, nil
}
