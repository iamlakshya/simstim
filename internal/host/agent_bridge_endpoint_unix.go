//go:build !windows

package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"

	"github.com/iamlakshya/simstim/internal/agentbridge"
	"github.com/iamlakshya/simstim/internal/paths"
)

func prepareHostAgentEndpoint(scope agentbridge.Scope) (*hostAgentEndpoint, error) {
	return prepareHostAgentEndpointContext(context.Background(), scope)
}

func prepareHostAgentEndpointContext(parent context.Context, scope agentbridge.Scope) (*hostAgentEndpoint, error) {
	ctx, cancel := context.WithTimeout(parent, agentLinkWait)
	defer cancel()
	if err := scope.Check(); err != nil {
		return nil, agentControlError()
	}
	root, err := paths.OpenPrivateRuntimeDir()
	if err != nil {
		return nil, agentSocketError()
	}
	endpoint := &hostAgentEndpoint{root: root}
	keep := false
	defer func() {
		if !keep {
			_ = endpoint.Close()
		}
	}()
	if err := recoverHostAgentEndpoints(ctx, root); err != nil {
		return nil, err
	}
	if err := claimHostAgentEndpoint(endpoint, scope.Lease); err != nil {
		return nil, err
	}
	for range 16 {
		var random [8]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, agentSocketError()
		}
		// This 19-byte basename fits wherever agent-<8-byte-id>.sock fits.
		name := "a" + hex.EncodeToString(random[:]) + ".s"
		path := filepath.Join(root.Name(), name)
		if len(path) > paths.MaxUnixSocketPath {
			return nil, agentSocketError()
		}
		if _, err := root.Lstat(name); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, agentSocketError()
		}
		if err := recordHostAgentEndpoint(endpoint, scope.Session, name); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		if err != nil {
			return nil, agentSocketError()
		}
		listener.SetUnlinkOnClose(false)
		endpoint.listener, endpoint.name, endpoint.path = listener, name, path
		created, statErr := root.Lstat(name)
		if statErr != nil || created.Mode()&os.ModeSocket == 0 {
			return nil, agentSocketError()
		}
		endpoint.info = created
		if err := retainHostAgentEndpoint(endpoint, scope.Lease); err != nil {
			return nil, err
		}
		keep = true
		return endpoint, nil
	}
	return nil, agentSocketError()
}

func claimHostAgentEndpoint(endpoint *hostAgentEndpoint, lease string) error {
	name := ".agent-claim-" + lease
	file, err := endpoint.root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL|agentNoFollow|agentNonblock, 0o600)
	if err != nil {
		return agentSocketError()
	}
	endpoint.claim, endpoint.claimName = file, name
	locked, lockErr := tryAgentLock(file)
	if lockErr != nil || !locked {
		return agentSocketError()
	}
	info, err := file.Stat()
	if err != nil || !privateAgentLock(info) {
		return agentSocketError()
	}
	endpoint.claimInfo = info
	current, err := endpoint.root.Lstat(name)
	if err != nil || !os.SameFile(current, info) {
		return agentSocketError()
	}
	return nil
}

func retainHostAgentEndpoint(endpoint *hostAgentEndpoint, lease string) error {
	root, name := endpoint.root, endpoint.name
	witness := ".agent-owned-" + lease
	if err := root.Link(name, witness); err != nil {
		return agentSocketError()
	}
	endpoint.witness = witness
	retained, err := root.Lstat(witness)
	if err != nil {
		return agentSocketError()
	}
	endpoint.info = retained
	current, err := root.Lstat(name)
	owner, ownerOK := retained.Sys().(*syscall.Stat_t)
	if err != nil || retained.Mode()&os.ModeSocket == 0 || !ownerOK || owner.Uid != uint32(os.Getuid()) || !os.SameFile(current, retained) {
		return agentSocketError()
	}
	opened, oerr := root.Stat(".")
	atPath, perr := os.Lstat(root.Name())
	if oerr != nil || perr != nil || !os.SameFile(opened, atPath) {
		return agentSocketError()
	}
	endpoint.bound = true
	if err := chmodAgentWitness(root, witness, retained); err != nil {
		return agentSocketError()
	}
	current, err = root.Lstat(name)
	if err != nil || !privateAgentSocket(current) || !os.SameFile(current, retained) {
		return agentSocketError()
	}
	return nil
}
