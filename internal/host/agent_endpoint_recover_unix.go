//go:build !windows

package host

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Recovery owns no process handle. A locked, versioned claim and a socket
// inode witness are the only authority; live, legacy and foreign entries
// survive. Directory batches keep unrelated runtime entries off the heap.
func recoverHostAgentEndpoints(ctx context.Context, root *os.Root) error {
	return walkAgentRuntime(ctx, root, func(name string) error {
		const prefix = ".agent-claim-"
		if !strings.HasPrefix(name, prefix) || len(name) != len(prefix)+32 || !agentLowerHex(name[len(prefix):]) {
			return nil
		}
		return recoverHostAgentEndpoint(ctx, root, name, name[len(prefix):])
	})
}

func walkAgentRuntime(ctx context.Context, root *os.Root, visit func(string) error) error {
	dir, err := root.Open(".")
	if err != nil {
		return agentSocketError()
	}
	defer dir.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := dir.ReadDir(32)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := visit(entry.Name()); err != nil {
				return err
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return agentSocketError()
		}
	}
}

func recoverHostAgentEndpoint(ctx context.Context, root *os.Root, name, lease string) error {
	before, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) || err == nil && !privateAgentLock(before) {
		return nil
	}
	if err != nil {
		return agentSocketError()
	}
	file, err := root.OpenFile(name, os.O_RDWR|agentNoFollow|agentNonblock, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return agentSocketError()
	}
	defer file.Close()
	locked, err := tryAgentLock(file)
	if err != nil {
		return agentSocketError()
	}
	if !locked {
		return nil
	}
	opened, err := file.Stat()
	current, cerr := root.Lstat(name)
	if err != nil || cerr != nil || !privateAgentLock(opened) || !os.SameFile(before, opened) || !os.SameFile(opened, current) {
		return nil
	}
	record, recognised, err := readAgentEndpointClaim(file)
	if err != nil || !recognised {
		return err
	}
	witnessName := ".agent-owned-" + lease
	witness, err := root.Lstat(witnessName)
	if errors.Is(err, os.ErrNotExist) {
		// A crash before the hardlink cannot prove who owns a socket name.
		return nil
	}
	if err != nil {
		return agentSocketError()
	}
	if witness.Mode()&os.ModeSocket == 0 || !privateAgentSocket(witness) {
		return nil
	}
	socket, err := root.Lstat(record.socket)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return agentSocketError()
	}
	if err == nil && !os.SameFile(socket, witness) {
		return nil
	}
	// Serialise with actual publication for this exact session. This also
	// protects a newer main link while the stale alias chain is removed.
	unlock, err := lockAgentLink(ctx, root, record.session)
	if err != nil {
		return err
	}
	defer unlock()
	complete, err := recoverAgentEndpointLinks(ctx, root, record, lease)
	if err != nil || !complete {
		return err
	}
	for _, entry := range []struct {
		name string
		info os.FileInfo
	}{{record.socket, witness}, {witnessName, witness}, {name, opened}} {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := removeAgentEndpointEntry(root, entry.name, entry.info); err != nil {
			return err
		}
	}
	return nil
}

func removeAgentEndpointEntry(root *os.Root, name string, expected os.FileInfo) error {
	current, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !os.SameFile(current, expected) {
		return agentSocketError()
	}
	if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return agentSocketError()
	}
	return nil
}

func recoverAgentEndpointLinks(ctx context.Context, root *os.Root, record agentEndpointClaim, lease string) (bool, error) {
	alias := ".agent-" + record.session + "-" + lease + ".sock"
	aliasWitness := ".agent-alias-owned-" + lease
	linkWitness := ".agent-publish-owned-" + lease
	aliasInfo, valid, err := agentPublicationRecoveryWitness(root, aliasWitness, filepath.Join(root.Name(), record.socket))
	if err != nil || !valid {
		return false, err
	}
	linkInfo, valid, err := agentPublicationRecoveryWitness(root, linkWitness, alias)
	if err != nil || !valid {
		return false, err
	}
	type entry struct {
		name, target   string
		proof, current os.FileInfo
	}
	entries := []entry{
		{name: ".agent-publish-" + record.session + "-" + lease + ".sock", target: alias, proof: linkInfo},
		{name: "agent-" + record.session + ".sock", target: alias, proof: linkInfo},
		{name: alias, target: filepath.Join(root.Name(), record.socket), proof: aliasInfo},
	}
	// Check all possible publication names before removing any. Missing
	// inode proof cannot be reconstructed from a target string or a prefix.
	aliasMatched, linkMatched := aliasInfo == nil, linkInfo == nil
	for i := range entries {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		e := &entries[i]
		info, err := root.Lstat(e.name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, agentSocketError()
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		target, err := root.Readlink(e.name)
		if err != nil {
			return false, agentSocketError()
		}
		if target != e.target {
			continue
		}
		if e.proof == nil {
			return false, nil
		}
		// A replacement is retained even when it repeats the old target.
		if os.SameFile(info, e.proof) {
			e.current = info
			if e.name == alias {
				aliasMatched = true
			} else {
				linkMatched = true
			}
		}
	}
	// A lone or replaced proof marker is preserved. It cannot establish
	// which missing namespace entry originally belonged to this attempt.
	if !aliasMatched || !linkMatched {
		return false, nil
	}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if e.current != nil {
			if err := removeAgentEndpointEntry(root, e.name, e.current); err != nil {
				return false, err
			}
		}
	}
	for _, e := range []entry{{name: linkWitness, proof: linkInfo}, {name: aliasWitness, proof: aliasInfo}} {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if e.proof != nil {
			if err := removeAgentEndpointEntry(root, e.name, e.proof); err != nil {
				return false, err
			}
		}
	}
	return true, nil
}

func agentPublicationRecoveryWitness(root *os.Root, name, target string) (os.FileInfo, bool, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, agentSocketError()
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if info.Mode()&os.ModeSymlink == 0 || !ok || owner.Uid != uint32(os.Getuid()) {
		return nil, false, nil
	}
	value, err := root.Readlink(name)
	if err != nil {
		return nil, false, agentSocketError()
	}
	if value != target {
		return nil, false, nil
	}
	return info, true, nil
}
