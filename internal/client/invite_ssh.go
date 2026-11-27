package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/join/code"
)

// JoinedSSHConfig renders a checked, explicit path. HopKnownHosts must name
// locally prepared pin files for every hop; it never accepts a path in a code.
// The final host's pin remains the normal host-add transaction's responsibility.
func JoinedSSHConfig(name, address, identity string, r code.Reply, hopKnownHosts []string) (string, string, error) {
	return joinedSSHConfigWithRouteV1(name, address, identity, r, hopKnownHosts, nil)
}
func joinedSSHConfigWithRouteV1(name, address, identity string, r code.Reply, hopKnownHosts []string, route *joinedRouteInvocationV1) (string, string, error) {
	if !ValidHostName(name) {
		return "", "", badHostNameError(name)
	}
	if err := r.Validate(); err != nil {
		return "", "", JoinError(err)
	}
	if !slices.Contains(r.Addresses, address) {
		return "", "", inviteError("bad_value", "the selected address is absent from the reply", "Use --address with an address printed in this machine's reply.")
	}
	if len(hopKnownHosts) != len(r.Hops) {
		return "", "", inviteError("invite_path", "each SSH hop needs its own checked host-key pin", "Check every host key on the machine's screen and retry the reply.")
	}
	alias := "joined-" + HostFileKey(name)
	var b strings.Builder
	fmt.Fprintf(&b, "%s%q. Do not edit; joined SSH path.\n", wrapperHeader, name)
	// These options occur before user/system includes in the ordinary wrapper.
	// Each spawned ProxyJump client reads this same explicit path.
	render := func(host, addr, user string, port uint16, pin string, jumps []string) error {
		fmt.Fprintf(&b, "Host %s\n    HostName %s\n    User %s\n    Port %d\n    CanonicalizeHostname no\n    BatchMode yes\n", host, addr, user, port)
		if identity != "" {
			if strings.ContainsAny(identity, "\r\n\x00") {
				return inviteError("invite_store", "the local identity path is invalid", "Make a fresh invite.")
			}
			fmt.Fprintf(&b, "    IdentityFile %q\n    IdentitiesOnly yes\n", identity)
		} else {
			b.WriteString("    IdentityFile none\n    IdentitiesOnly yes\n    IdentityAgent none\n")
		}
		if pin != "" {
			if !filepath.IsAbs(pin) || strings.ContainsAny(pin, "\r\n\x00") {
				return inviteError("invite_path", "a hop pin path is invalid", "Prepare the hop's local host-key pin again.")
			}
			fmt.Fprintf(&b, "    HostKeyAlias %s\n    UserKnownHostsFile %q\n    GlobalKnownHostsFile %s\n    StrictHostKeyChecking yes\n    UpdateHostKeys no\n    VerifyHostKeyDNS no\n    CheckHostIP no\n    NoHostAuthenticationForLocalhost no\n    KnownHostsCommand none\n    FingerprintHash sha256\n    LogLevel ERROR\n", host, pin, os.DevNull)
		}
		if len(jumps) > 0 {
			fmt.Fprintf(&b, "    ProxyJump %s\n    ProxyCommand none\n", strings.Join(jumps, ","))
		} else if route != nil {
			command, err := route.proxyCommandV1(addr, port)
			if err != nil {
				return err
			}
			fmt.Fprintf(&b, "    ProxyCommand %s\n    ProxyJump none\n", command)
		} else {
			b.WriteString("    ProxyJump none\n    ProxyCommand none\n")
		}
		b.WriteString("\n")
		return nil
	}
	var jumps []string
	for n, hop := range r.Hops {
		hopAlias := fmt.Sprintf("%s-hop-%d", alias, n+1)
		if err := render(hopAlias, hop.Addresses[0], hop.Account, hop.Port, hopKnownHosts[n], jumps); err != nil {
			return "", "", err
		}
		jumps = append(jumps, hopAlias)
	}
	if err := render(alias, address, r.Account, r.Port, "", jumps); err != nil {
		return "", "", err
	}
	return b.String(), alias, nil
}

// InviteSSHStage owns the generated config until normal host add saves it.
// A declined or failed addition removes only its original native files.
type InviteSSHStage struct {
	path            string
	data            []byte
	committed       bool
	children        []*InviteSSHStage
	info            os.FileInfo
	directory       string
	directoryInfo   os.FileInfo
	ownership       *joinedOwnershipStage
	route           *joinedRouteRecordV1
	routeProjection *explicitRouteProjectionV1
	identityPublic  bool
	acceptance      *InviteAcceptance
	originalHost    Host
	hops            []code.Hop
}

// StageInviteSSH writes only validated fields to a local generated config.
func StageInviteSSH(ctx context.Context, inv HostInvite, r code.Reply, address string) (Host, *InviteSSHStage, error) {
	return stageInviteSSHWithRouteV1(ctx, inv, r, address, nil)
}
func stageInviteSSHWithRouteV1(ctx context.Context, inv HostInvite, r code.Reply, address string, projection *explicitRouteProjectionV1) (Host, *InviteSSHStage, error) {
	var h Host
	if err := ctx.Err(); err != nil {
		return h, nil, err
	}

	if _, _, err := JoinedSSHConfig(inv.Name, address, inv.IdentityFile, r, make([]string, len(r.Hops))); err != nil {
		return h, nil, err
	}
	identity := inv.IdentityFile
	var publicIdentity string
	if identity == "" {
		var err error
		publicIdentity, err = code.PublicKey(inv.PublicKey)
		if err != nil {
			return h, nil, JoinError(err)
		}
	}
	dir := filepath.Join(ConfigDir(), "ssh")
	if err := privateInviteDir(dir); err != nil {
		return h, nil, err
	}
	ownedDir, err := os.MkdirTemp(dir, "joined-"+HostFileKey(inv.Name)+"-")
	if err != nil {
		return h, nil, err
	}
	directoryInfo, err := os.Lstat(ownedDir)
	if err != nil {
		return h, nil, err
	}
	path := filepath.Join(ownedDir, "config")
	st := &InviteSSHStage{path: path, directory: ownedDir, directoryInfo: directoryInfo, hops: cloneJoinedHops(r.Hops)}
	hopPaths := make([]string, len(r.Hops))
	for n := range hopPaths {
		hopPaths[n] = filepath.Join(ownedDir, fmt.Sprintf("hop-%d.keys", n+1))
	}
	if identity == "" {
		identity = filepath.Join(ownedDir, "identity.pub")
		child, err := stageInviteFile(identity, []byte(publicIdentity+"\n"))
		if err != nil {
			return h, st, errors.Join(err, st.Close())
		}
		st.children = append(st.children, child)
		st.identityPublic = true
	}
	var invocation *joinedRouteInvocationV1
	if projection != nil {
		st.routeProjection = projection
		invocation, err = projection.publishIntoStageV1(ctx, st)
		if err != nil {
			return h, st, joinRouteErrorsV1(err, st.Close())
		}
		record := invocation.record
		st.route = &record
	}
	activePaths := make([]string, len(r.Hops))
	text, alias, err := joinedSSHConfigWithRouteV1(inv.Name, address, identity, r, activePaths, invocation)
	if err != nil {
		_ = os.Remove(ownedDir)
		return h, nil, err
	}
	if err = st.write([]byte(text)); err != nil {
		_ = os.Remove(ownedDir)
		return h, nil, err
	}
	h = Host{Name: inv.Name, SSH: alias, SSHConfig: path, Labels: inv.Labels}
	for n, hop := range r.Hops {
		hopHost := h
		hopHost.SSH = fmt.Sprintf("%s-hop-%d", alias, n+1)
		keys, err := st.readJoinedHopKeysV1(ctx, hopHost, hop.Fingerprints)
		if err != nil {
			return Host{}, nil, errors.Join(err, st.Close())
		}
		var lines strings.Builder
		for _, key := range keys {
			lines.WriteString(key.line(hopHost.SSH))
			lines.WriteByte('\n')
		}
		child, err := stageInviteFile(hopPaths[n], []byte(lines.String()))
		if err != nil {
			return Host{}, nil, errors.Join(err, st.Close())
		}
		st.children = append(st.children, child)
		activePaths[n] = hopPaths[n]
		updated, _, err := joinedSSHConfigWithRouteV1(inv.Name, address, identity, r, activePaths, invocation)
		if err != nil {
			return Host{}, nil, errors.Join(err, st.Close())
		}
		if err = st.write([]byte(updated)); err != nil {
			return Host{}, nil, errors.Join(err, st.Close())
		}
	}
	own, err := st.createOwnership(ctx, h)
	if err != nil {
		if own != nil {
			st.ownership = own
		}
		return Host{}, st, errors.Join(err, st.Close())
	}
	st.ownership = own
	record := own.record
	h.JoinedSSH = &record
	return h, st, nil
}

// Commit keeps the exact generated file referenced by the saved host.
func (s *InviteSSHStage) Commit() {
	s.committed = true
	if s.ownership != nil {
		s.ownership.committed = true
	}
	for _, child := range s.children {
		child.Commit()
	}
}

// Close restores only this operation's unchanged generated file on failure.
func (s *InviteSSHStage) Close() error {
	if s == nil {
		return nil
	}
	if s.committed {
		if s.ownership != nil {
			s.ownership.close()
		}
		return nil
	}
	if s.ownership != nil {
		if err := s.ownership.abort(); err != nil {
			return retainJoinedStage(s, err)
		}
		s.ownership.close()
		s.ownership = nil
	}
	var childErr error
	for n := len(s.children) - 1; n >= 0; n-- {
		childErr = errors.Join(childErr, s.children[n].Close())
	}
	s.children = nil
	current, statErr := os.Lstat(s.path)
	if s.info == nil && errors.Is(statErr, os.ErrNotExist) && s.directory != "" {
		here, err := os.Lstat(s.directory)
		if err != nil || !os.SameFile(here, s.directoryInfo) {
			return errors.Join(childErr, hostStateRemovalError("the original unfilled joined directory changed"))
		}
		if childErr != nil {
			return childErr
		}
		return os.Remove(s.directory)
	}
	if statErr != nil || s.info == nil || !os.SameFile(s.info, current) {
		return errors.Join(childErr, inviteError("invite_store", "the original generated SSH file was replaced", "Keep the replacement and retry with a fresh invite."))
	}
	if s.directory != "" {
		current, err := os.Lstat(s.directory)
		if err != nil || !os.SameFile(s.directoryInfo, current) {
			return errors.Join(childErr, inviteError("invite_store", "the original generated SSH folder was replaced", "Keep the folder and inspect the interrupted add."))
		}
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	if string(b) != string(s.data) {
		return inviteError("invite_store", "the generated SSH file changed during the add", "Inspect the file before retrying the reply.")
	}
	if err = os.Remove(s.path); err != nil {
		return errors.Join(childErr, err)
	}
	if s.directory != "" {
		return errors.Join(childErr, os.Remove(s.directory))
	}
	return childErr
}

// InviteAddress picks an explicit reply address, otherwise a tailnet DNS name,
// otherwise its first address. All choices have already passed field checks.
func InviteAddress(r code.Reply, explicit string) (string, error) {
	if err := r.Validate(); err != nil {
		return "", JoinError(err)
	}
	if explicit != "" {
		if !slices.Contains(r.Addresses, explicit) {
			return "", inviteError("bad_value", "--address does not name an address in the reply", "Choose one of the machine's printed addresses.")
		}
		return explicit, nil
	}
	for _, address := range r.Addresses {
		if strings.HasSuffix(strings.ToLower(address), ".ts.net") {
			return address, nil
		}
	}
	return r.Addresses[0], nil
}

func stageInviteFile(path string, data []byte) (*InviteSSHStage, error) {
	st := &InviteSSHStage{path: path}
	if err := st.write(data); err != nil {
		return nil, err
	}
	return st, nil
}

// write captures the actual atomic publisher's inode, never a matching path
// or byte sequence observed after publication. Subsequent writes and undo
// retain that same original ownership through every progressive hop pin.
func (s *InviteSSHStage) write(data []byte) error {
	expected := s.info
	hooks := atomicFileHooks{beforeReplace: func(root *os.Root, base string) error {
		if s.directory != "" {
			here, err := root.Stat(".")
			if err != nil || !os.SameFile(s.directoryInfo, here) {
				return inviteError("invite_store", "the original generated SSH folder changed", "Retry with a fresh invite.")
			}
		}
		current, err := root.Lstat(base)
		if expected == nil {
			if !errors.Is(err, os.ErrNotExist) {
				return inviteError("invite_store", "the new generated SSH path was filled by another writer", "Retry with a fresh invite.")
			}
			return nil
		}
		if err != nil || !current.Mode().IsRegular() || !os.SameFile(expected, current) {
			return inviteError("invite_store", "the original generated SSH file changed", "Retry with a fresh invite.")
		}
		return nil
	}, published: func(info os.FileInfo) { s.info = info }}
	if err := writeAtomicFileWithHooks(s.path, ".joined-ssh-*", data, hooks); err != nil {
		return err
	}
	s.data = append([]byte(nil), data...)
	return nil
}
