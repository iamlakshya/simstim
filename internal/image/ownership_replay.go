package image

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
)

// loadOwnership is called only with the platform's existing exclusive lease.
// It can reconstruct a lost receipt head from coherent exact-native intents.
// Ambiguous generations and unknown protocol files remain untouched.
func (s *Store) loadOwnership(ctx context.Context, digest string) (imageOwnership, error) {
	receipt, err := s.readOwnership(digest)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return receipt, err
	}
	root, err := s.ownershipRoot()
	if err != nil {
		return receipt, err
	}
	defer root.Close()
	names, err := directoryEntries(root)
	if err != nil {
		return receipt, err
	}
	type candidate struct {
		name    string
		receipt imageOwnership
		rank    int
		witness imageFileWitness
	}
	var candidates []candidate
	for _, name := range names {
		if !strings.HasPrefix(name, ".receipt-new-") && !strings.HasPrefix(name, ".receipt-old-") {
			continue
		}
		data, witness, readErr := readReceipt(root, name)
		if readErr != nil {
			continue
		}
		var value imageOwnership
		if json.Unmarshal(data, &value) != nil || !value.known() || value.Digest != digest || value.Store.CheckRoot(s.root) != nil || s.checkDomain(value.Domain) != nil {
			continue
		}
		rank := s.replayRank(value)
		if rank > 0 {
			candidates = append(candidates, candidate{name: name, receipt: value, rank: rank, witness: witness})
		}
	}
	if len(candidates) == 0 {
		return receipt, os.ErrNotExist
	}
	chosen := candidates[0]
	for _, value := range candidates[1:] {
		if value.receipt.Directory.Identity != chosen.receipt.Directory.Identity || value.receipt.Disk.Ref.Identity != chosen.receipt.Disk.Ref.Identity {
			return receipt, ownershipError("receipt replay contains multiple native image generations")
		}
		if value.rank > chosen.rank {
			chosen = value
		}
	}
	if err := ctx.Err(); err != nil {
		return receipt, err
	}
	current, err := checkWitness(root, chosen.name, chosen.witness, 0o600)
	if err != nil {
		return receipt, err
	}
	current.Close()
	if err := s.writeOwnership(chosen.receipt); err != nil {
		return receipt, err
	}
	for _, value := range candidates {
		// The exact snapshot must still agree before removing an obsolete protocol
		// backup. It is never recursive cleanup or authority over image data.
		file, err := checkWitness(root, value.name, value.witness, 0o600)
		if err != nil {
			return receipt, err
		}
		file.Close()
		if err := root.Remove(value.name); err != nil {
			return receipt, err
		}
	}
	if err := syncRoot(root); err != nil {
		return receipt, err
	}
	return chosen.receipt, nil
}

func (s *Store) replayRank(receipt imageOwnership) int {
	locations := []string{receipt.Leaf}
	if stageLeaf(receipt.Leaf) {
		locations = append(locations, receipt.Digest[7:])
	}
	if receipt.Purge != nil {
		locations = append(locations, receipt.Purge.Quarantine)
	}
	for _, leaf := range locations {
		root, err := s.ownedDirectory(receipt, leaf, receipt.Purge != nil)
		if err != nil {
			continue
		}
		root.Close()
		rank := 1
		if receipt.Leaf == receipt.Digest[7:] {
			rank = 2
		}
		if receipt.Pending != nil {
			rank = 3
		}
		if receipt.Purge != nil {
			rank = 4
		}
		if receipt.Cleanup != "" {
			rank = 5
		}
		return rank
	}
	if receipt.Purge != nil {
		for _, leaf := range locations {
			if _, err := s.root.Lstat(leaf); !errors.Is(err, os.ErrNotExist) {
				return 0
			}
		}
		if receipt.Cleanup != "" {
			return 5
		}
		return 4
	}
	return 0
}
