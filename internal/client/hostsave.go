package client

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/iamlakshya/simstim/internal/brand"
)

// One change of the saved entry of one host (docs/contributing/cli-spec.md
// 11.3, rule 13). Each command that saves or forgets a host names the
// entry that it read before it began, and the change goes ahead only while
// the saved entry is still that one: a compare and swap, under the lock of
// hosts.toml. When another command changed the host meanwhile, the change
// fails with host_changed, and changes nothing. So a removed host never
// comes back, and a host made again is never overwritten, however long
// the command took.

// A HostChange is one change of the saved entry of one host: the save of
// Now, or, with Now nil, the forget of the host.
type HostChange struct {
	// Kind names the command, such as "host set", for the lock of the
	// host and for the error.
	Kind string
	// Was is the entry that the command read before it began, or nil when
	// no host of the name was saved.
	Was *Host
	// Now is the entry to save, or nil to forget the host.
	Now *Host
	// Pin binds this save to its operation-owned staged pin or current pin.
	Pin bool
	// Tier sets the saved host tier alongside its entry.
	Tier Tier
	// HostID records the verified identity alongside the saved entry.
	HostID string
}

// errNoEntry is the error of a change without an entry, which names no
// host.
var errNoEntry = errors.New("a change of a host needs the entry that it saves or forgets")

// name is the name of the host that c changes.
func (c HostChange) name() string {
	if c.Now != nil {
		return c.Now.Name
	}
	if c.Was != nil {
		return c.Was.Name
	}
	return ""
}

// SavedEntry is the saved entry of host name, or nil when no host of the
// name is saved: the Was of a change.
func SavedEntry(name string) (*Host, error) {
	h, err := FindHost(name)
	var unknown *UnknownNameError
	switch {
	case errors.As(err, &unknown):
		return nil, nil
	case err != nil:
		return nil, err
	}
	return &h, nil
}

// UpdateHosts makes change c. It takes the lock of the host for c.Kind
// for that moment, unless ctx holds it already: no change comes while
// another command, such as host rm at its question, holds it. Under the
// lock of hosts.toml it compares the saved entry with c.Was, and saves
// nothing when they differ. A forget removes the host's generated SSH
// config first: when that fails, the host is still saved, so the command
// can run again.
func UpdateHosts(ctx context.Context, c HostChange) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.Was != nil {
		previous := cloneObservationHost(*c.Was)
		c.Was = &previous
	}
	if c.Now != nil {
		next := cloneObservationHost(*c.Now)
		c.Now = &next
	}
	name := c.name()
	if name == "" {
		return errNoEntry
	}
	if c.Now != nil && !ValidHostName(name) {
		return badHostNameError(name)
	}
	st, err := stageForContext(ctx, name)
	withPin := c.Now != nil && (c.Pin || st != nil)
	if err != nil && !withPin {
		return err
	}
	stageErr := err
	if withPin {
		defer abortPinStage(ctx, name, st)
	}
	if stageErr == nil {
		var endPublication func()
		ctx, endPublication, err = withHostPublicationObservation(ctx, c)
		if err != nil {
			return err
		}
		defer endPublication()
	}
	if ValidHostName(name) {
		var unlock func()
		ctx, unlock, err = LockHost(ctx, name, c.Kind)
		if err != nil {
			return err
		}
		defer unlock()
	}
	if stageErr != nil {
		// A retired stage cannot hide a changed saved entry. Compare under
		// the same file lock before migration or any other side-file write;
		// an unchanged entry still receives the captured stage's refusal.
		return updateHostsFile(ctx, func(hosts []Host) ([]Host, hostFileHooks, error) {
			if _, err := ApplyHostChange(hosts, c); err != nil {
				return nil, hostFileHooks{}, err
			}
			return nil, hostFileHooks{}, stageErr
		})
	}
	if ValidHostName(name) {
		var endMutation func()
		ctx, endMutation, err = holdHostMutationContext(ctx, name)
		if err != nil {
			return err
		}
		defer endMutation()
	}
	if c.Now != nil {
		if err := ensureTiersMigrated(ctx); err != nil {
			return err
		}
	} else if ValidHostName(name) {
		if err := migratePinContext(ctx, name); err != nil {
			return err
		}
	}
	if ValidHostName(name) && (c.Now == nil || withPin) {
		unlockPin, err := lockPinAs(ctx, name, true)
		if err != nil {
			return err
		}
		defer unlockPin()
	}
	return updateHostsFileContext(ctx, func(locked context.Context, hosts []Host) ([]Host, hostFileHooks, error) {
		if err := checkHostPublicationObservation(locked, c); err != nil {
			return nil, hostFileHooks{}, err
		}
		return changeHostFiles(locked, c, hosts, withPin, st)
	})
}

func badHostNameError(name string) error {
	return &UserError{Msg: fmt.Sprintf("%q cannot name a host", CleanLine(name)), Hint: "Fix the host's name in " + hostsPath() + ".", Code: "bad_name", Kind: KindUsage}
}

// ApplyHostChange makes change c in hosts, the list that its caller read,
// and returns the new list. hosts stays as it was. When the entry of the
// host in hosts is not c.Was, it changes nothing, and fails with
// host_changed. UpdateHosts uses it, and so do the launcher's fakes.
func ApplyHostChange(hosts []Host, c HostChange) ([]Host, error) {
	name := c.name()
	if name == "" {
		return nil, errNoEntry
	}
	var now *Host
	out := make([]Host, 0, len(hosts)+1)
	for i := range hosts {
		if hosts[i].Name == name {
			now = &hosts[i]
			continue
		}
		out = append(out, hosts[i])
	}
	if !sameOrNone(now, c.Was) {
		return nil, c.changed(now)
	}
	if c.Now != nil {
		out = append(out, *c.Now)
	}
	return out, nil
}

// SameEntry reports whether a and b are one saved entry: every field the
// same, the machine's created stamp too. An empty table or list counts
// the same as none, as the save of hosts.toml writes neither.
func SameEntry(a, b Host) bool { return reflect.DeepEqual(entryOf(a), entryOf(b)) }

// entryOf is h as SameEntry compares it.
func entryOf(h Host) Host {
	h.machineSourceBirth = nil // Ephemeral capability is checked separately, never saved-entry data.
	if len(h.Labels) == 0 {
		h.Labels = nil
	}
	if len(h.Secrets) == 0 {
		h.Secrets = nil
	}
	return h
}

// sameOrNone reports whether a and b are one entry, or both none.
func sameOrNone(a, b *Host) bool {
	if a == nil || b == nil {
		return a == b
	}
	return SameEntry(*a, *b)
}

// changed is the error of change c when the saved entry of its host is
// now, not c.Was: another command removed the host, added it, made it
// again or changed it while c's command ran. A new host whose name another
// command took meanwhile is host_exists.
func (c HostChange) changed(now *Host) error {
	name := c.name()
	if c.Was == nil && now != nil {
		return &UserError{Msg: fmt.Sprintf("another command added a host named %s while %s ran, so this one was not saved", name, c.Kind),
			Hint: brand.Name + " host show " + name + " (or pick another name)", Code: "host_exists"}
	}
	what, look := "changed", brand.Name+" host show "+name
	switch {
	case now == nil:
		what, look = "was removed", brand.Name+" host ls"
	case !SameMachine(now.Machine, c.Was.Machine):
		what = "was made again"
	}
	not := "saved"
	if c.Now == nil {
		not = "forgotten"
	}
	return &UserError{Msg: fmt.Sprintf("%s %s while %s ran, so it was not %s", name, what, c.Kind, not),
		Hint: look + " (then run the command again)", Code: "host_changed"}
}
