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
	rollback   func()
	commit     func()
	release    func()
	failure    func(error) error
	beforeSave func() error
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
	unlock, err := lockHostsFile(ctx)
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
	out, hooks, err := change(hosts)
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
	if err := writeHosts(out); err != nil {
		return hooks.failed(err)
	}
	if hooks.commit != nil {
		hooks.commit()
	}
	return nil
}

// beforeHostsSave runs in updateHostsFile just before the save, and
// writeHosts saves the list. Tests replace them.
var (
	beforeHostsSave = func() {}
	writeHosts      = writeHostsFile
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
	hosts = slices.Clone(hosts)
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].Name < hosts[j].Name })
	var b bytes.Buffer
	fmt.Fprintf(&b, "# Hosts for %s. Edit with `%s host`.\n\n", brand.DisplayName, brand.Name)
	if err := toml.NewEncoder(&b).Encode(hostsFile{Hosts: hosts}); err != nil {
		return fmt.Errorf("save the hosts: %w", err)
	}
	return writeAtomicFile(hostsPath(), ".hosts.toml-*", b.Bytes())
}
