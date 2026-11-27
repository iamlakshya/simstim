package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/agentbridge"
	"github.com/iamlakshya/simstim/internal/paths"
)

// The claim already binds this nonce to one locked endpoint. Retained
// hardlinks pin both symlink inodes across crashes and prevent a later
// recovery from treating a same-target replacement as the original link.
func publishAgentEndpointLinkAt(ctx context.Context, owned *os.Root, id, target, nonce string) (*agentLease, error) {
	if (agentbridge.Scope{Session: id, Lease: nonce}).Check() != nil || !agentRuntimeAtPath(owned) {
		return nil, agentSocketError()
	}
	r, err := owned.OpenRoot(".")
	if err != nil {
		return nil, agentSocketError()
	}
	keep := false
	defer func() {
		if !keep {
			_ = r.Close()
		}
	}()
	name := "agent-" + id + ".sock"
	if len(filepath.Join(r.Name(), name)) > paths.MaxUnixSocketPath {
		return nil, agentSocketError()
	}
	unlock, err := lockAgentLink(ctx, r, id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if !agentRuntimeAtPath(r) {
		return nil, agentSocketError()
	}
	if info, err := r.Lstat(name); err == nil && info.Mode()&os.ModeSymlink == 0 {
		return nil, agentSocketError()
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, agentSocketError()
	}
	l := &agentLease{root: r, name: name, alias: ".agent-" + id + "-" + nonce + ".sock", target: target,
		aliasWitness: ".agent-alias-owned-" + nonce, linkWitness: ".agent-publish-owned-" + nonce, done: make(chan struct{})}
	if err := r.Symlink(target, l.alias); err != nil {
		return nil, agentSocketError()
	}
	// After the first namespace write, return the partial lease on errors.
	// Its caller owns exact cleanup and must retain proof if that fails.
	keep = true
	l.aliasInfo, err = retainAgentPublicationLink(r, l.alias, l.aliasWitness)
	if err != nil {
		// The claim/socket proof remains available to recovery. Never erase
		// an unverified name merely because this attempt created its prefix.
		return l, err
	}
	temp := ".agent-publish-" + id + "-" + nonce + ".sock"
	l.temp = temp
	if err := r.Symlink(l.alias, temp); err != nil {
		return l, agentSocketError()
	}
	l.info, err = retainAgentPublicationLink(r, temp, l.linkWitness)
	if err != nil {
		return l, err
	}
	if err := ctx.Err(); err != nil {
		return l, err
	}
	if err := r.Rename(temp, name); err != nil {
		return l, agentSocketError()
	}
	return l, nil
}

func retainAgentPublicationLink(root *os.Root, name, witness string) (os.FileInfo, error) {
	before, err := root.Lstat(name)
	if err != nil || before.Mode()&os.ModeSymlink == 0 {
		return nil, agentSocketError()
	}
	if err := root.Link(name, witness); err != nil {
		return before, agentSocketError()
	}
	retained, err := root.Lstat(witness)
	current, cerr := root.Lstat(name)
	if err != nil || cerr != nil || !os.SameFile(before, retained) || !os.SameFile(retained, current) {
		return before, agentSocketError()
	}
	return retained, nil
}
