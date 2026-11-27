package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// joinedSSHRemovalV1 is an opaque retained native consumer. Opening it grants
// no unlink; only the actual generic forget producer may bind and commit it.
// All paths and expected bytes come from its actual saved Source and writer.
type joinedSSHRemovalV1 struct {
	preparation           *savedHostRemovalPreparationV1
	original              Host
	hostID                string
	root                  *os.Root
	directory             string
	record                JoinedSSHOwnership
	manifest              joinedOwnershipManifest
	manifestFile          *os.File
	files                 []*os.File
	closed                bool
	ready                 bool
	moved                 []bool
	leafRemoved           []bool
	anchorRemoved         []bool
	journal               *os.File
	journalInfo           os.FileInfo
	generation            string
	finalising            bool
	finalised             bool
	manifestLeafRemoved   bool
	manifestAnchorRemoved bool
	journalRemoved        bool
	directoryRemoved      bool
}

// prepareJoinedSSHRemovalV1 runs in the generic producer's original locked
// saved-Source transaction. It cannot accept a Host, path or manifest claim.
func prepareJoinedSSHRemovalV1(ctx context.Context, phase *savedHostRemovalPreparationV1) (result *joinedSSHRemovalV1, failure error) {
	if phase == nil {
		return nil, hostStateRemovalError("joined cleanup needs its original generic saved removal producer")
	}
	if err := phase.Check(ctx); err != nil {
		return nil, err
	}
	if phase.Consumer() != "joined-ssh-v1" {
		return nil, hostStateRemovalError("the joined consumer has another native purpose")
	}
	if err := requireHostMembershipContext(ctx); err != nil {
		return nil, err
	}
	h := phase.Host()
	if h.JoinedSSH == nil {
		return nil, nil
	}
	if !ValidHostID(phase.HostID()) {
		return nil, hostStateRemovalError("joined cleanup needs the actual original HostID")
	}

	if !validJoinedRecord(h.JoinedSSH) {
		return nil, hostStateRemovalError("the joined ownership reference is unknown or malformed")
	}
	r, err := openJoinedSSHOriginV1(h, phase.HostID(), true)
	if err != nil {
		return r, err
	}
	r.preparation = phase
	if err = phase.Check(ctx); err != nil {
		return nil, errors.Join(err, r.close())
	}
	return r, nil
}

// This private native reader grants no deletion. Its callers retain the
// actual original saved Source before and after opening these immutable files.
func openJoinedSSHOriginV1(h Host, hostID string, recoverPreparation bool) (result *joinedSSHRemovalV1, failure error) {
	directory := filepath.Dir(h.SSHConfig)
	parent := filepath.Join(ConfigDir(), "ssh")
	if filepath.Base(h.SSHConfig) != "config" || filepath.Dir(directory) != parent || !strings.HasPrefix(filepath.Base(directory), "joined-"+HostFileKey(h.Name)+"-") {
		return nil, hostStateRemovalError("joined cleanup refuses a caller-selected config path")
	}
	root, err := cachefs.OpenPrivateRoot(directory, false)
	if err != nil {
		return nil, err
	}
	r := &joinedSSHRemovalV1{original: cloneObservationHost(h), hostID: hostID, root: root, directory: directory, record: *h.JoinedSSH}
	keep := false
	defer func() {
		if !keep {
			failure = errors.Join(failure, r.close())
		}
	}()
	f, err := root.Open("ownership.json")
	if err != nil {
		return nil, err
	}
	r.manifestFile = f
	body, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 1<<20 || joinedDigest(body) != r.record.ManifestSHA256 {
		return nil, hostStateRemovalError("the joined manifest's original committed bytes changed")
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err = d.Decode(&r.manifest); err != nil {
		return nil, err
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF {
		return nil, hostStateRemovalError("the joined manifest has trailing data")
	}
	m := r.manifest
	if m.Version != 1 || m.Commit != r.record.Commit || m.Name != h.Name || m.Target != h.SSH || m.ManifestNative != r.record.ManifestNative || len(m.Files) < 1 || len(m.Files) > 15 || len(m.Files) != joinedExpectedFileCountV1(m) {
		return nil, hostStateRemovalError("the joined manifest belongs to another original target")
	}
	for _, hop := range m.Hops {
		if err = hop.Validate(); err != nil {
			return nil, JoinError(err)
		}
	}

	if err = m.Directory.CheckRoot(root); err != nil {
		return nil, err
	}
	if err = checkJoinedPair(root, joinedOwnedFile{"ownership.json", r.record.ManifestNative, r.record.ManifestSHA256}, f, 2); err != nil {
		return nil, err
	}
	r.moved = make([]bool, len(m.Files))
	r.leafRemoved = make([]bool, len(m.Files))
	r.anchorRemoved = make([]bool, len(m.Files))
	if recoverPreparation {
		if err = r.recoverJoinedPreparation(); err != nil {
			return nil, err
		}
	} else if _, err = root.Lstat("cleanup.json"); !errors.Is(err, os.ErrNotExist) {
		return nil, hostStateRemovalError("the joined path has an active removal journal")
	}
	seen := map[string]bool{}
	for n, pair := range m.Files {
		if !joinedLeaf(pair.Leaf) || seen[pair.Leaf] {
			return nil, hostStateRemovalError("the joined manifest repeats or invents a leaf role")
		}
		seen[pair.Leaf] = true
		file, err := root.Open(pair.Leaf)
		if errors.Is(err, os.ErrNotExist) && r.journal != nil {
			file, err = root.Open(retiredJoinedLeaf(pair.Leaf))
			if err == nil {
				r.moved[n] = true
			}
		}
		if err != nil {
			return nil, err
		}
		r.files = append(r.files, file)
		if r.moved[n] {
			err = checkJoinedPairNames(root, pair, file, retiredJoinedLeaf(pair.Leaf), joinedAnchor(pair.Leaf), 2)
		} else {
			err = checkJoinedPair(root, pair, file, 2)
		}
		if err != nil {
			return nil, err
		}
	}
	if !seen["config"] {
		return nil, hostStateRemovalError("the joined manifest has no original config")
	}
	if m.IdentityPublic && !seen["identity.pub"] {
		return nil, hostStateRemovalError("the joined manifest lost its original offered public identity")
	}
	for index := range m.Hops {
		if !seen[fmt.Sprintf("hop-%d.keys", index+1)] {
			return nil, hostStateRemovalError("the joined manifest lost an original reply hop")
		}
	}
	if m.Route != nil {
		if !seen["route.conf"] {
			return nil, hostStateRemovalError("the joined manifest lost its original route projection")
		}
		for index := 0; index < m.Route.Bastions; index++ {
			if !seen[fmt.Sprintf("route-bastion-%d.keys", index+1)] {
				return nil, hostStateRemovalError("the joined manifest lost an original bastion pin")
			}
		}
	}

	r.ready = true
	keep = true
	return r, nil
}

func (r *joinedSSHRemovalV1) checkRetained() error {
	if r == nil || r.closed || r.root == nil {
		return hostStateRemovalError("the original joined removal consumer ended")
	}
	if err := r.manifest.Directory.CheckRoot(r.root); err != nil {
		return err
	}
	if len(r.files) != len(r.manifest.Files) {
		return hostStateRemovalError("the original joined descriptors are incomplete")
	}
	for n, pair := range r.manifest.Files {
		var err error
		if r.moved[n] {
			err = checkJoinedPairNames(r.root, pair, r.files[n], retiredJoinedLeaf(pair.Leaf), joinedAnchor(pair.Leaf), 2)
		} else {
			err = checkJoinedPair(r.root, pair, r.files[n], 2)
		}
		if err != nil {
			return err
		}
	}
	return checkJoinedPair(r.root, joinedOwnedFile{"ownership.json", r.record.ManifestNative, r.record.ManifestSHA256}, r.manifestFile, 2)
}

func joinedExpectedFileCountV1(manifest joinedOwnershipManifest) int {
	identity := 0
	if manifest.IdentityPublic {
		identity = 1
	}
	if manifest.Route == nil {
		return 1 + len(manifest.Hops) + identity
	}
	if manifest.Route.Version != 1 || manifest.Route.Bastions < 1 || manifest.Route.Bastions > 8 {
		return -1
	}
	return 2 + len(manifest.Hops) + manifest.Route.Bastions + identity
}
