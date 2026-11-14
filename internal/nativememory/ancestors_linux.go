//go:build linux

package nativememory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
)

func (s *currentLinux) ancestorHeadroom(ctx context.Context) (Limit, error) {
	combined := Limit{kind: Unlimited}
	parentEnabled := false
	for i, group := range s.groups {
		if err := ctx.Err(); err != nil {
			return Limit{}, err
		}
		controllers, err := memoryControllerRole(ctx, group, i == 0)
		if err != nil {
			return Limit{}, err
		}
		// Only the actual visible root or a child whose parent explicitly
		// did not enable memory can have no local memory interface. This
		// says nothing about ancestors hidden outside a cgroup namespace.
		local, err := groupHeadroom(ctx, group, i == 0 || !parentEnabled)
		if err != nil {
			return Limit{}, err
		}
		combined, err = tighter(combined, local)
		if err != nil {
			return Limit{}, err
		}
		parentEnabled = slices.Contains(controllers, "memory")
	}
	return combined, nil
}

// memoryControllerRole verifies real kernel role files and returns the
// enabled subtree controllers for the next actual child in this chain.
func memoryControllerRole(ctx context.Context, group *nativeFile, visibleRoot bool) ([]string, error) {
	typeBytes, err := readAccountingBytes(ctx, group, "cgroup.type")
	if err != nil && !(visibleRoot && errors.Is(err, os.ErrNotExist)) {
		return nil, errors.Join(ErrUnknown, err)
	}
	// The kernel does not expose cgroup.type on the actual hierarchy root.
	// A missing type in any non-root role remains unknown, never unlimited.
	if err == nil && strings.TrimSpace(string(typeBytes)) != "domain" {
		return nil, fmt.Errorf("threaded or unknown memory domain: %w", ErrUnsupported)
	}
	if _, err := readAccountingBytes(ctx, group, "cgroup.controllers"); err != nil {
		return nil, errors.Join(ErrUnknown, err)
	}
	subtree, err := readAccountingBytes(ctx, group, "cgroup.subtree_control")
	if err != nil {
		return nil, errors.Join(ErrUnknown, err)
	}
	return strings.Fields(string(subtree)), nil
}

func groupHeadroom(ctx context.Context, group *nativeFile, absentRole bool) (out Limit, failure error) {
	maximum, err := openAccountingFile(group, "memory.max")
	if errors.Is(err, os.ErrNotExist) {
		return absentGroupLimit(group, absentRole)
	}
	if err != nil {
		return Limit{}, errors.Join(ErrUnknown, err)
	}
	defer func() { failure = errors.Join(failure, maximum.file.Close()) }()
	limit, err := ReadLimit(ctx, maximum.file)
	if err != nil {
		return Limit{}, err
	}
	current, err := openAccountingFile(group, "memory.current")
	if err != nil {
		return Limit{}, errors.Join(ErrUnknown, err)
	}
	defer func() { failure = errors.Join(failure, current.file.Close()) }()
	used, err := ReadScalar(ctx, current.file)
	if err != nil {
		return Limit{}, err
	}
	if err := errors.Join(maximum.check(), current.check(), ctx.Err()); err != nil {
		return Limit{}, errors.Join(ErrUnknown, err)
	}
	return remaining(limit, used)
}

func absentGroupLimit(group *nativeFile, absentRole bool) (Limit, error) {
	if !absentRole {
		return Limit{}, fmt.Errorf("enabled memory controller has no limit file: %w", ErrUnknown)
	}
	current, err := openAccountingFile(group, "memory.current")
	if err == nil {
		return Limit{}, errors.Join(ErrUnknown, current.file.Close())
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Limit{}, errors.Join(ErrUnknown, err)
	}
	return Limit{kind: Unlimited}, nil // Known no-local-limit role, not global ancestry.
}
