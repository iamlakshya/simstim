package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/join/code"
)

// JoinedSSHOwnership is local ownership data emitted by the original joined
// config writer. It grants no connection, execution, forget or deletion.
type JoinedSSHOwnership struct {
	Version        int    `toml:"version" json:"version"`
	Commit         string `toml:"commit" json:"commit"`
	ManifestSHA256 string `toml:"manifest_sha256" json:"manifest_sha256"`
	ManifestNative string `toml:"manifest_native" json:"manifest_native"`
}
type joinedOwnedFile struct {
	Leaf   string `json:"leaf"`
	Native string `json:"native"`
	SHA256 string `json:"sha256"`
}
type joinedOwnershipManifest struct {
	Version        int                  `json:"version"`
	Commit         string               `json:"commit"`
	Name           string               `json:"name"`
	Target         string               `json:"target"`
	Directory      cachefs.Reference    `json:"directory"`
	ManifestNative string               `json:"manifest_native"`
	Files          []joinedOwnedFile    `json:"files"`
	Hops           []code.Hop           `json:"hops,omitempty"`
	IdentityPublic bool                 `json:"identity_public,omitempty"`
	Route          *joinedRouteRecordV1 `json:"route,omitempty"`
}
type joinedOwnershipStage struct {
	root         *os.Root
	directory    cachefs.Reference
	manifest     *os.File
	manifestInfo os.FileInfo
	manifestPair bool
	files        []joinedOwnedFile
	record       JoinedSSHOwnership
	committed    bool
}

func joinedDigest(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func validJoinedDigest(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func validJoinedRecord(r *JoinedSSHOwnership) bool {
	return r != nil && r.Version == 1 && len(r.Commit) == 26 && validJoinedDigest(r.ManifestSHA256) && len(r.ManifestNative) > 0 && len(r.ManifestNative) < 128
}
func joinedLeaf(leaf string) bool {
	if leaf == "config" || leaf == "route.conf" || leaf == "identity.pub" {
		return true
	}
	for index := 1; index <= 8; index++ {
		if leaf == fmt.Sprintf("route-bastion-%d.keys", index) {
			return true
		}
		if index <= 4 && leaf == fmt.Sprintf("hop-%d.keys", index) {
			return true
		}
	}
	return false
}

// createOwnership retains actual native hardlink births for every immutable
// generated leaf. It is called only on the original in-memory writer stage.
func (s *InviteSSHStage) createOwnership(ctx context.Context, h Host) (result *joinedOwnershipStage, failure error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := cachefs.OpenPrivateRoot(s.directory, false)
	if err != nil {
		return nil, err
	}
	own := &joinedOwnershipStage{root: root}
	keep := false
	defer func() {
		if !keep {
			if err := own.abort(); err != nil {
				result = own
				failure = errors.Join(failure, err)
				return
			}
			own.close()
		}
	}()
	here, err := root.Stat(".")
	if err != nil || !os.SameFile(s.directoryInfo, here) {
		return nil, hostStateRemovalError("the original joined directory changed before ownership publication")
	}
	own.directory, err = cachefs.CaptureRoot(root)
	if err != nil {
		return nil, err
	}
	stages := append([]*InviteSSHStage{s}, s.children...)
	for _, fileStage := range stages {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		leaf := filepath.Base(fileStage.path)
		if !joinedLeaf(leaf) || filepath.Dir(fileStage.path) != s.directory {
			return nil, hostStateRemovalError("a joined ownership leaf has another role or root")
		}
		file, err := cachefs.OpenFile(root, leaf, os.O_RDONLY, 0o600)
		if err != nil {
			return nil, err
		}
		info, statErr := file.Stat()
		if statErr != nil || !os.SameFile(fileStage.info, info) {
			file.Close()
			return nil, hostStateRemovalError("the original joined publisher's inode changed")
		}
		native, err := joinedNativeIdentity(file, 1)
		if err != nil {
			file.Close()
			return nil, err
		}
		b, err := io.ReadAll(io.LimitReader(file, int64(len(fileStage.data))+1))
		if err != nil || !bytes.Equal(b, fileStage.data) {
			file.Close()
			return nil, hostStateRemovalError("the original joined publisher's bytes changed")
		}
		if err = root.Link(leaf, leaf+".origin"); err != nil {
			file.Close()
			return nil, err
		}
		pair := joinedOwnedFile{Leaf: leaf, Native: native, SHA256: joinedDigest(fileStage.data)}
		own.files = append(own.files, pair)
		err = checkJoinedPair(root, pair, file, 2)
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			return nil, errors.Join(err, closeErr)
		}
	}
	f, err := root.OpenFile("ownership.json", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	own.manifest = f
	own.manifestInfo, err = f.Stat()
	if err != nil {
		return nil, err
	}
	native, err := joinedNativeIdentity(f, 1)
	if err != nil {
		return nil, err
	}
	manifest := joinedOwnershipManifest{Version: 1, Commit: rand.Text(), Name: h.Name, Target: h.SSH, Directory: own.directory, ManifestNative: native, Files: own.files, Hops: cloneJoinedHops(s.hops), Route: s.route, IdentityPublic: s.identityPublic}
	body, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	body = append(body, '\n')
	if _, err = f.Write(body); err != nil {
		return nil, err
	}
	if err = f.Sync(); err != nil {
		return nil, err
	}
	if err = root.Link("ownership.json", "ownership.origin"); err != nil {
		return nil, err
	}
	own.manifestPair = true
	manifestPair := joinedOwnedFile{Leaf: "ownership.json", Native: native, SHA256: joinedDigest(body)}
	if err = checkJoinedPair(root, manifestPair, f, 2); err != nil {
		return nil, err
	}
	own.record = JoinedSSHOwnership{1, manifest.Commit, manifestPair.SHA256, native}
	keep = true
	return own, nil
}
func checkJoinedPair(root *os.Root, pair joinedOwnedFile, file *os.File, links uint64) error {
	native, err := joinedNativeIdentity(file, links)
	if err != nil || native != pair.Native {
		return hostStateRemovalError("a joined native file origin changed")
	}
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	for _, leaf := range []string{pair.Leaf, joinedAnchor(pair.Leaf)} {
		info, err := root.Lstat(leaf)
		if err != nil || !info.Mode().IsRegular() || !os.SameFile(opened, info) {
			return hostStateRemovalError("a joined file or its original anchor was replaced")
		}
	}
	b, err := io.ReadAll(io.NewSectionReader(file, 0, 1<<20))
	if err != nil {
		return err
	}
	if !validJoinedDigest(pair.SHA256) || joinedDigest(b) != pair.SHA256 {
		return hostStateRemovalError("a joined file's committed bytes changed")
	}
	return nil
}
func joinedAnchor(leaf string) string {
	if leaf == "ownership.json" {
		return "ownership.origin"
	}
	return leaf + ".origin"
}
func (s *joinedOwnershipStage) abort() error {
	if s == nil || s.committed {
		return nil
	}
	if err := s.directory.CheckRoot(s.root); err != nil {
		return err
	}
	if s.manifest != nil {
		info, err := s.root.Lstat("ownership.json")
		if err != nil || !os.SameFile(s.manifestInfo, info) {
			return hostStateRemovalError("the joined manifest changed before abort")
		}
		links := uint64(1)
		if s.manifestPair {
			links = 2
		}
		if _, err = joinedNativeIdentity(s.manifest, links); err != nil {
			return err
		}
		if s.manifestPair {
			anchor, err := s.root.Lstat("ownership.origin")
			if err != nil || !os.SameFile(info, anchor) {
				return hostStateRemovalError("the joined manifest anchor changed before abort")
			}
			if err = s.root.Remove("ownership.origin"); err != nil {
				return err
			}
			s.manifestPair = false
		}
		if err = s.root.Remove("ownership.json"); err != nil {
			return err
		}
		if err = s.manifest.Close(); err != nil {
			return err
		}
		s.manifest = nil
	}
	for n := len(s.files) - 1; n >= 0; n-- {
		pair := s.files[n]
		f, err := s.root.Open(pair.Leaf)
		if err != nil {
			return err
		}
		err = checkJoinedPair(s.root, pair, f, 2)
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			return errors.Join(err, closeErr)
		}
		if err = s.root.Remove(joinedAnchor(pair.Leaf)); err != nil {
			return err
		}
		s.files = s.files[:n]
	}
	return nil
}
func (s *joinedOwnershipStage) close() {
	if s == nil {
		return
	}
	if s.manifest != nil {
		_ = s.manifest.Close()
		s.manifest = nil
	}
	if s.root != nil {
		_ = s.root.Close()
		s.root = nil
	}
}

// checkJoinedIntroductionLocked is a native writer guard, not a new authority
// constructor. No raw Host or manifest reference may introduce ownership data.
func checkJoinedIntroductionLocked(ctx context.Context, c HostChange) error {
	if c.Now == nil {
		if c.Was != nil && c.Was.JoinedSSH != nil {
			return requireSavedHostRemovalParticipantV1(ctx, *c.Was, "joined-ssh-v1")
		}
		return nil
	}
	if c.Now.JoinedSSH == nil {
		if c.Was != nil && c.Was.JoinedSSH != nil {
			return hostStateRemovalError("a saved joined owner cannot be dropped outside its actual forget")
		}
		return nil
	}
	if c.Was != nil && sameJoinedRecord(c.Was.JoinedSSH, c.Now.JoinedSSH) {
		if c.Was.SSH != c.Now.SSH || c.Was.SSHConfig != c.Now.SSHConfig {
			return hostStateRemovalError("an original joined route cannot be replaced by a metadata edit")
		}
		return nil
	}
	if err := requireHostMembershipContext(ctx); err != nil {
		return err
	}
	a, _ := ctx.Value(inviteAcceptanceKey{}).(*InviteAcceptance)
	if a == nil || a.stage == nil || a.stage.ownership == nil || a.unlock == nil || c.Was != nil || !ValidHostID(c.HostID) || !SameEntry(*c.Now, a.expected) || !sameJoinedRecord(c.Now.JoinedSSH, &a.stage.ownership.record) {
		return hostStateRemovalError("joined ownership needs its original native registration writer")
	}
	return a.stage.ownership.checkCurrent()
}
func sameJoinedRecord(a, b *JoinedSSHOwnership) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
func (s *joinedOwnershipStage) checkCurrent() error {
	if s == nil || s.root == nil {
		return hostStateRemovalError("the original joined ownership stage ended")
	}
	if err := s.directory.CheckRoot(s.root); err != nil {
		return err
	}
	for _, pair := range s.files {
		f, err := s.root.Open(pair.Leaf)
		if err != nil {
			return err
		}
		err = checkJoinedPair(s.root, pair, f, 2)
		err = errors.Join(err, f.Close())
		if err != nil {
			return err
		}
	}
	if s.manifest == nil {
		return hostStateRemovalError("the original joined manifest owner ended")
	}
	return checkJoinedPair(s.root, joinedOwnedFile{"ownership.json", s.record.ManifestNative, s.record.ManifestSHA256}, s.manifest, 2)
}

// Bulk metadata replacement may preserve a writer-owned reference, but cannot
// introduce, change or abandon one. Actual joined forget uses its one generic
// original saved-membership transaction and the separately narrowed consumer.
func checkJoinedBulkRows(old, next []Host) error {
	for _, h := range next {
		if h.JoinedSSH == nil {
			continue
		}
		matched := false
		for _, was := range old {
			if was.Name == h.Name && was.SSH == h.SSH && was.SSHConfig == h.SSHConfig && sameJoinedRecord(was.JoinedSSH, h.JoinedSSH) {
				matched = true
				break
			}
		}
		if !matched {
			return hostStateRemovalError("bulk metadata cannot introduce joined SSH ownership")
		}
	}
	for _, was := range old {
		if was.JoinedSSH == nil {
			continue
		}
		retained := false
		for _, h := range next {
			if h.Name == was.Name && h.SSH == was.SSH && h.SSHConfig == was.SSHConfig && sameJoinedRecord(was.JoinedSSH, h.JoinedSSH) {
				retained = true
				break
			}
		}
		if !retained {
			return hostStateRemovalError("bulk metadata cannot drop an original joined SSH owner")
		}
	}
	return nil
}

func cloneJoinedHops(hops []code.Hop) []code.Hop {
	if len(hops) == 0 {
		return nil
	}
	out := make([]code.Hop, len(hops))
	for n, h := range hops {
		out[n] = h
		out[n].Addresses = append([]string(nil), h.Addresses...)
		out[n].Fingerprints = append([]string(nil), h.Fingerprints...)
	}
	return out
}
