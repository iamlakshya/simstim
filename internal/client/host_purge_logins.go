package client

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/agents"
)

func purgeLoginPathsV1() ([]string, error) {
	var paths []string
	seen := map[string]bool{}
	for _, agent := range agents.Table {
		if agent.Setup == nil {
			continue
		}
		for _, path := range agent.Setup.LoginFiles {
			if !strings.HasPrefix(path, "~/") || strings.ContainsAny(path, "\x00\r\n") || strings.Contains(path, "/../") || strings.HasSuffix(path, "/..") {
				return nil, grantOperationError()
			}
			if !seen[path] {
				seen[path] = true
				paths = append(paths, path)
			}
		}
	}
	slices.Sort(paths)
	return paths, nil
}

func purgeLoginCommandV1(paths []string, remove bool) string {
	var command strings.Builder
	command.WriteString("set -eu\nPATH=/usr/bin:/bin\nexport PATH\n")
	for index, path := range paths {
		fmt.Fprintf(&command, "p=\"$HOME\"/%s\n", ShellQuote(strings.TrimPrefix(path, "~/")))
		fmt.Fprintf(&command, "if [ -L \"$p\" ] || { [ -e \"$p\" ] && [ ! -f \"$p\" ]; }; then printf '%%s\\tunknown\\n' %d\n", index)
		fmt.Fprintf(&command, "elif [ -f \"$p\" ]; then ")
		state := "present"
		if remove {
			command.WriteString("/bin/rm -- \"$p\"; ")
			state = "removed"
		}
		fmt.Fprintf(&command, "printf '%%s\\t%s\\n' %d\nelse printf '%%s\\tabsent\\n' %d\nfi\n", state, index, index)
	}
	return command.String()
}

func parsePurgeLoginRowsV1(raw []byte, paths []string, remove bool) ([]string, error) {
	if len(raw) > 16<<10 {
		return nil, hostStateRemovalError("the login check exceeded its fixed bound")
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(paths) == 0 && len(raw) == 0 {
		return nil, nil
	}
	if len(lines) != len(paths) {
		return nil, hostStateRemovalError("the login check returned an incomplete fixed file list")
	}
	var found []string
	for index, line := range lines {
		fields := strings.Split(line, "\t")
		if len(fields) != 2 || fields[0] != strconv.Itoa(index) {
			return nil, hostStateRemovalError("the login check returned a different file order")
		}
		switch fields[1] {
		case "absent":
		case "present":
			if remove {
				return nil, grantOperationError()
			}
			found = append(found, paths[index])
		case "removed":
			if !remove {
				return nil, grantOperationError()
			}
		case "unknown":
			found = append(found, paths[index]+" (preserved: linked or unknown file type)")
		default:
			return nil, hostStateRemovalError("the login check returned an unknown result")
		}
	}
	return found, nil
}

func PurgeLoginFiles(ctx context.Context) ([]string, error) {
	p, err := purgeOperationOfV1(ctx)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	record := p.receipt
	p.mu.Unlock()
	if !record.has("root") {
		return nil, grantOperationError()
	}
	if record.has("logins") {
		return slices.Clone(record.Report.Logins), nil
	}
	paths, err := purgeLoginPathsV1()
	if err != nil {
		return nil, err
	}
	raw, err := p.runFixedNative(ctx, purgeLoginCommandV1(paths, false), nil, 16<<10)
	if err != nil {
		return nil, err
	}
	found, err := parsePurgeLoginRowsV1(raw, paths, false)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.receipt.Report.Logins = slices.Clone(found)
	p.mu.Unlock()
	return found, p.save(ctx)
}

func PurgeAgentLogins(ctx context.Context, remove bool) error {
	p, err := purgeOperationOfV1(ctx)
	if err != nil {
		return err
	}
	p.mu.Lock()
	record := p.receipt
	p.mu.Unlock()
	if record.has("logins") {
		return nil
	}
	if !record.has("root") || record.DeleteLogins != nil && *record.DeleteLogins != remove {
		return grantOperationError()
	}
	p.mu.Lock()
	decision := remove
	p.receipt.DeleteLogins = &decision
	p.mu.Unlock()
	if err := p.save(ctx); err != nil {
		return err
	}
	if remove {
		paths, err := purgeLoginPathsV1()
		if err != nil {
			return err
		}
		raw, err := p.runFixedNative(ctx, purgeLoginCommandV1(paths, true), nil, 16<<10)
		if err != nil {
			return err
		}
		if _, err := parsePurgeLoginRowsV1(raw, paths, true); err != nil {
			return err
		}
	}
	return p.finish(ctx, "logins")
}
