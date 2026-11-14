package client

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"sort"

	"github.com/BurntSushi/toml"

	"github.com/iamlakshya/simstim/internal/brand"
)

// hostFileHooks finish a side-file transaction before releasing its lock.
type hostFileHooks struct {
	membershipGeneration       string
	membershipSourceGeneration string
	rollback                   func()
	commit                     func()
	release                    func()
	failure                    func(error) error
	beforeSave                 func() error
}

// updateHostsFile reads the saved hosts, lets change make the new list,
// and saves it, under one lock of hosts.toml. So two commands that change
// two hosts at once never lose one change: each reads what the other
// saved. The lock is held only around the read, the change and the save,
// and always after the lock of a host, never before it. A command that
// was cancelled, as at Ctrl+C, changes and saves nothing: ctx is checked
// before the change and again before the save. rollback, when set, undoes
// a side file that change put in place before the save failed.
func updateHostsFile(ctx context.Context, change func([]Host) ([]Host, hostFileHooks, error)) error {
	return updateHostsFileContext(ctx, func(_ context.Context, hosts []Host) ([]Host, hostFileHooks, error) { return change(hosts) })
}

// beforeHostsSave runs in updateHostsFile just before the save, and
// writeHosts saves the list. Tests replace them.
var (
	beforeHostsSave              = func() {}
	writeHosts                   = writeHostsFile
	writeHostsSelectedGeneration = writeHostsFileGeneration
)

// failed restores this transaction before describing any retained safety state.
func (h hostFileHooks) failed(err error) error {
	if h.rollback != nil {
		h.rollback()
	}
	if h.failure != nil {
		return h.failure(err)
	}
	return err
}

func writeHostsFile(hosts []Host) error {
	generation, err := newMembershipGeneration()
	if err != nil {
		return fmt.Errorf("make the host membership generation: %w", err)
	}
	return writeHostsFileGeneration(hosts, generation)
}

func updateHostsFileContext(ctx context.Context, change func(context.Context, []Host) ([]Host, hostFileHooks, error)) error {
	ctx, unlock, err := acquireHostMembershipContext(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	hosts, err := LoadHosts()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	out, hooks, err := change(ctx, hosts)
	if hooks.release != nil {
		defer hooks.release()
	}
	if err != nil {
		return hooks.failed(err)
	}
	beforeHostsSave()
	if err := ctx.Err(); err != nil {
		return hooks.failed(err)
	}
	if hooks.beforeSave != nil {
		if err := hooks.beforeSave(); err != nil {
			return hooks.failed(err)
		}
	}
	if err := ctx.Err(); err != nil {
		return hooks.failed(err)
	}
	if hooks.membershipGeneration != "" {
		actual, err := currentMembershipGeneration(ctx)
		if err != nil || actual != hooks.membershipSourceGeneration {
			return hooks.failed(hostStateRemovalError("saved hosts changed before the prepared removal committed"))
		}
	}
	var writeErr error
	if hooks.membershipGeneration != "" {
		writeErr = writeHostsSelectedGeneration(out, hooks.membershipGeneration)
	} else {
		writeErr = writeHosts(out)
	}
	if writeErr != nil {
		return hooks.failed(writeErr)
	}
	if hooks.commit != nil {
		hooks.commit()
	}
	return nil
}

func writeHostsFileGeneration(hosts []Host, generation string) error {
	if !ValidHostID(generation) {
		return hostStateRemovalError("the selected host membership generation is invalid")
	}
	hosts = slices.Clone(hosts)
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].Name < hosts[j].Name })
	var b bytes.Buffer
	b.WriteString(membershipGenerationPrefix + generation + "\n")
	fmt.Fprintf(&b, "# Hosts for %s. Edit with `%s host`.\n\n", brand.DisplayName, brand.Name)
	if err := toml.NewEncoder(&b).Encode(hostsFile{Hosts: hosts}); err != nil {
		return fmt.Errorf("save the hosts: %w", err)
	}
	return writeAtomicFile(hostsPath(), ".hosts.toml-*", b.Bytes())
}
