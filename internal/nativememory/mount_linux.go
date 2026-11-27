//go:build linux

package nativememory

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

type memoryMount struct {
	id         uint64
	root, path string
}

func currentUnifiedPath(raw []byte) (string, error) {
	membership := ParseMembership(raw)
	if membership.Malformed {
		return "", ErrUnknown
	}
	var path string
	for _, row := range membership.Records {
		if slices.Contains(row.Controllers, "memory") {
			return "", fmt.Errorf("legacy memory hierarchy: %w", ErrUnsupported)
		}
		if row.Hierarchy != "0" || len(row.Controllers) != 0 {
			continue
		}
		if path != "" || !closedNativePath(row.Path) {
			return "", ErrUnknown
		}
		path = row.Path
	}
	if path == "" {
		return "", ErrUnsupported
	}
	return path, nil
}

func relativeGroup(root, member string) (string, bool) {
	if !closedNativePath(root) || !closedNativePath(member) {
		return "", false
	}
	if root == "/" {
		return strings.TrimPrefix(member, "/"), true
	}
	if member == root {
		return "", true
	}
	return strings.TrimPrefix(member, root+"/"), strings.HasPrefix(member, root+"/")
}

// findMemoryMount streams actual mountinfo and retains only the widest
// visible unified root. A clipped namespace remains a clipped observation;
// the caller's actual root/ancestor receipt must establish completeness.
func findMemoryMount(ctx context.Context, input io.Reader, member string) (memoryMount, error) {
	scanner := bufio.NewScanner(contextReader{ctx: ctx, in: input})
	var best memoryMount
	for scanner.Scan() {
		mount, unified, err := memoryMountLine(scanner.Text())
		if err != nil {
			return memoryMount{}, err
		}
		if !unified {
			continue
		}
		if _, matches := relativeGroup(mount.root, member); !matches {
			continue
		}
		if best.id == 0 || len(mount.root) < len(best.root) {
			best = mount
		}
	}
	if err := errors.Join(scanner.Err(), ctx.Err()); err != nil {
		return memoryMount{}, errors.Join(ErrUnknown, err)
	}
	if best.id == 0 {
		return memoryMount{}, fmt.Errorf("current unified hierarchy is not mapped: %w", ErrUnknown)
	}
	return best, nil
}

func memoryMountLine(line string) (memoryMount, bool, error) {
	before, after, found := strings.Cut(line, " - ")
	if !found {
		return memoryMount{}, false, ErrUnknown
	}
	right := strings.Fields(after)
	if len(right) < 3 {
		return memoryMount{}, false, ErrUnknown
	}
	if right[0] != "cgroup2" {
		return memoryMount{}, false, nil
	}
	left := strings.Fields(before)
	if len(left) < 6 {
		return memoryMount{}, false, ErrUnknown
	}
	id, err := decimal(left[0])
	if err != nil || id == 0 {
		return memoryMount{}, false, errors.Join(ErrUnknown, err)
	}
	root, err := mountPath(left[3])
	if err != nil {
		return memoryMount{}, false, err
	}
	path, err := mountPath(left[4])
	if err != nil {
		return memoryMount{}, false, err
	}
	return memoryMount{id: id, root: root, path: path}, true, nil
}

func mountPath(encoded string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(encoded); i++ {
		if encoded[i] != '\\' {
			out.WriteByte(encoded[i])
			continue
		}
		if i+3 >= len(encoded) {
			return "", ErrUnknown
		}
		switch encoded[i+1 : i+4] {
		case "040":
			out.WriteByte(' ')
		case "011":
			out.WriteByte('\t')
		case "012":
			out.WriteByte('\n')
		case "134":
			out.WriteByte('\\')
		default:
			return "", ErrUnknown
		}
		i += 3
	}
	path := out.String()
	if !closedNativePath(path) {
		return "", ErrUnknown
	}
	return path, nil
}
