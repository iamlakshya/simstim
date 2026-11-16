package files

import (
	"fmt"
	"slices"
	"strings"
)

// ExcludeGeneratedHomeState restricts an existing state plan with canonical
// generated home paths. It drops only exact copied-file declarations. A kept
// directory or any ancestor/descendant overlap is refused, never silently
// narrowed. It opens, restores, changes and deletes no filesystem object.
func ExcludeGeneratedHomeState(plan StatePlan, paths []string) (StatePlan, error) {
	if err := plan.Validate(); err != nil {
		return StatePlan{}, fmt.Errorf("invalid state plan before generated-file exclusion: %w. Try: use a valid disjoint state plan before starting the sandbox", err)
	}
	generated := make(map[string]bool, len(paths))
	for _, name := range paths {
		if !Relative(name) || generated[name] {
			return StatePlan{}, fmt.Errorf("invalid or repeated generated home path. Try: use the canonical generated-file data without rewriting its paths")
		}
		generated[name] = true
	}
	for _, directory := range plan.Dirs {
		for _, name := range paths {
			if generatedPathOverlap(directory, name) {
				return StatePlan{}, fmt.Errorf("kept directory overlaps a generated home file. Try: choose disjoint state directories instead of retaining a generated-file parent or child")
			}
		}
	}
	output := StatePlan{Dirs: slices.Clone(plan.Dirs), Files: slices.Clone(plan.Files)}
	output.Files = output.Files[:0]
	for _, file := range plan.Files {
		if generated[file] {
			continue
		}
		for _, name := range paths {
			if generatedPathOverlap(file, name) {
				return StatePlan{}, fmt.Errorf("kept file declaration overlaps a generated home file. Try: keep only disjoint file declarations instead of a generated-file ancestor or child")
			}
		}
		output.Files = append(output.Files, file)
	}
	return output, nil
}

func generatedPathOverlap(first, second string) bool {
	return first == second || strings.HasPrefix(first, second+"/") || strings.HasPrefix(second, first+"/")
}
