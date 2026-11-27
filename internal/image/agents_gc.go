package image

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"

	"github.com/iamlakshya/simstim/internal/supply"
)

type agentsCandidate struct{ Digest, Metadata string }

func (s *Store) agentsCandidates(root *os.Root) ([]agentsCandidate, error) {
	names, err := directoryEntries(root)
	if err != nil {
		return nil, err
	}
	byDigest := map[string]string{}
	for _, name := range names {
		digest := strings.TrimSuffix(name, ".json")
		if strings.HasSuffix(name, ".json") && supply.IsHex64(digest) {
			if previous := byDigest[digest]; previous != "" {
				return nil, agentsDiskError("multiple agents metadata generations remain; all were preserved")
			}
			byDigest[digest] = name
			continue
		}
		if !nonceLeaf(name, ".agents-meta-purge-") {
			continue
		}
		raw, _, err := readReceipt(root, name)
		if err != nil {
			return nil, err
		}
		var head struct {
			Disk AgentDiskV1 `json:"disk"`
		}
		if json.Unmarshal(raw, &head) != nil {
			return nil, agentsDiskError("a captured agents removal record is unknown")
		}
		record, _, err := s.readAgentsRecordAt(root, name, head.Disk.Content.SHA256)
		if err != nil {
			return nil, err
		}
		if record.Purge == nil || record.Purge.Metadata != name {
			return nil, agentsDiskError("a captured agents record has another removal identity")
		}
		if previous := byDigest[record.Disk.Content.SHA256]; previous != "" {
			return nil, agentsDiskError("both original and captured agents metadata remain")
		}
		byDigest[record.Disk.Content.SHA256] = name
	}
	result := make([]agentsCandidate, 0, len(byDigest))
	for digest, name := range byDigest {
		result = append(result, agentsCandidate{digest, name})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Digest < result[j].Digest })
	return result, nil
}

// PruneAgentsV1 retains newest and every nondeleted trusted session reference.
// Busy disks are retained, including an unpublished client-selected lease.
// Each exclusive candidate rereads authoritative references and newest before
// deletion. Unknown metadata or reference authority fails collection closed.
func (s *Store) PruneAgentsV1(ctx context.Context, refs References) ([]string, error) {
	if refs == nil {
		return nil, agentsDiskError("trusted agents references are unavailable")
	}
	if err := s.requireWritableHost(); err != nil {
		return nil, err
	}
	root, err := s.agentsRoot(ctx, false)
	if errors.Is(err, os.ErrNotExist) {
		if _, inspectErr := s.root.Lstat("agents"); inspectErr == nil {
			return nil, agentsDiskError("an existing agents child has no canonical directory receipt; it was preserved")
		} else if !errors.Is(inspectErr, os.ErrNotExist) {
			return nil, inspectErr
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	candidates, err := s.agentsCandidates(root)
	if err != nil {
		return nil, err
	}
	removed := []string{}
	for _, candidate := range candidates {
		used, err := refs()
		if err != nil {
			return removed, err
		}
		if used[candidate.Digest] != "" {
			continue
		}
		lease, acquired, err := s.tryExistingLease(ctx, "agents/"+candidate.Digest+".lock", false)
		if err != nil {
			return removed, err
		}
		if !acquired {
			continue
		}
		did, err := s.collectAgentsLocked(ctx, root, candidate, refs, true)
		closeErr := lease.Close()
		if did {
			removed = append(removed, candidate.Digest)
		}
		if err = errors.Join(err, closeErr); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

func (s *Store) collectAgentsLocked(ctx context.Context, root *os.Root, candidate agentsCandidate, refs References, retainNewest bool) (bool, error) {
	source, err := s.existingLease(ctx, "agents.lock", false)
	if err != nil {
		return false, err
	}
	defer source.Close()
	used, err := refs()
	if err != nil {
		return false, err
	}
	if used[candidate.Digest] != "" {
		return false, nil
	}
	if retainNewest {
		newest, err := s.readAgentsSource(root)
		if err != nil {
			return false, err
		}
		if newest.Disk.Content.SHA256 == candidate.Digest {
			return false, nil
		}
	}
	record, metadata, err := s.readAgentsRecordAt(root, candidate.Metadata, candidate.Digest)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	remove := func() error { return s.removeAgentsRecord(ctx, root, candidate.Metadata, record, metadata) }
	if retainNewest {
		err = s.withPublication(ctx, remove)
	} else {
		err = remove()
	}
	return err == nil, err
}
