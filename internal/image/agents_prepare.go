package image

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/supply"
)

// AgentsBuildResultV1 describes the actual scoped producer's source snapshot.
// Reused is permitted only for the verified previous source passed to Build.
type AgentsBuildResultV1 struct {
	SourceDigest string
	Agents       []string
	Reused       bool
}

// AgentsBuilderV1 uses a fresh output FD, the canonical owned guardian and the
// no-network converter jail. No image-store lease is held while it runs.
type AgentsBuilderV1 func(context.Context, string, *os.File) (AgentsBuildResultV1, error)

// EnsureAgentsV1 snapshots actual installed agents and returns exact producer
// inventory with a shared disk lease. Host consumers retain that lease until
// their same protected writer has durably recorded the selected ContentRef.
func (s *Store) EnsureAgentsV1(ctx context.Context, arch string, build AgentsBuilderV1) (disk AgentDiskV1, retained *Lease, returned error) {
	if err := s.domain.Validate(); err != nil {
		return disk, nil, err
	}
	if s.gate == nil || build == nil || arch != "amd64" && arch != "arm64" {
		return disk, nil, agentsDiskError("the canonical host producer, placement gate or architecture is missing")
	}
	initial, err := s.beginAgentsSource(ctx)
	if err != nil {
		return disk, nil, err
	}
	previous := ""
	if initial != nil {
		file, lease, err := s.OpenAgentsV1(ctx, initial.Disk.Content)
		if err != nil {
			return disk, nil, err
		}
		if err := errors.Join(file.Close(), lease.Close()); err != nil {
			return disk, nil, err
		}
		previous = initial.Disk.SourceDigest
	}
	name, err := randomLeaf(".agents-part-")
	if err != nil {
		return disk, nil, err
	}
	output, err := s.regular("agents/"+name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return disk, nil, err
	}
	part, err := s.captureCreated("agents/"+name, output, 0o600)
	if err != nil {
		return disk, nil, errors.Join(err, output.Close())
	}
	mode := os.FileMode(0o600)
	handedOff := false
	defer func() {
		if !handedOff {
			returned = errors.Join(returned, s.cleanupCreated(part, mode))
		}
		returned = errors.Join(returned, output.Close())
		if returned != nil && retained != nil {
			returned = errors.Join(returned, retained.Close())
			retained = nil
		}
	}()
	result, err := build(ctx, previous, output)
	if err != nil {
		return disk, nil, err
	}
	if err := ctx.Err(); err != nil {
		return disk, nil, err
	}
	disk = AgentDiskV1{Version: 1, Arch: arch, SourceDigest: result.SourceDigest, Agents: slices.Clone(result.Agents)}
	if disk.Agents == nil {
		disk.Agents = []string{}
	}
	if result.Reused {
		if initial == nil || previous == "" || result.SourceDigest != previous {
			return disk, nil, agentsDiskError("the producer reused an unverified source")
		}
		disk.Content = initial.Disk.Content
	} else {
		disk.Content, err = agentsOutputContent(ctx, output)
		if err != nil {
			return disk, nil, err
		}
		if err := output.Chmod(0o444); err != nil {
			return disk, nil, err
		}
		mode = 0o444
		if err := output.Sync(); err != nil {
			return disk, nil, err
		}
	}
	if err := disk.Validate(arch); err != nil {
		return disk, nil, err
	}
	retained, err = s.prepareAgentsPublication(ctx, initial, disk, name, output, result.Reused, &handedOff)
	return disk, retained, err
}

func (s *Store) beginAgentsSource(ctx context.Context) (*agentsSourceV1, error) {
	lease, err := s.lock(ctx, "agents.lock", false)
	if err != nil {
		return nil, err
	}
	defer lease.Close()
	root, err := s.agentsRoot(ctx, true)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	source, err := s.readAgentsSource(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return &source, err
}

func agentsOutputContent(ctx context.Context, output *os.File) (supply.ContentRef, error) {
	size, err := InspectAgentsV1(output)
	if err != nil {
		return supply.ContentRef{}, err
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, &flatContextReader{ctx: ctx, r: io.NewSectionReader(output, 0, size)}); err != nil {
		return supply.ContentRef{}, err
	}
	return supply.ContentRef{Name: "agents", SHA256: hex.EncodeToString(digest.Sum(nil)), Size: size}, ctx.Err()
}

func (s *Store) commitAgentsSourceLocked(ctx context.Context, initial *agentsSourceV1, disk AgentDiskV1, temporary string, output *os.File, reused bool, handedOff *bool) error {
	// The caller already holds the disk lease; collection uses the same
	// disk-before-source order. No converter runs under either lease.
	sourceLease, err := s.lock(ctx, "agents.lock", false)
	if err != nil {
		return err
	}
	defer sourceLease.Close()
	root, err := s.agentsRoot(ctx, false)
	if err != nil {
		return err
	}
	defer root.Close()
	current, err := s.readAgentsSource(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && current.Disk.SourceDigest != disk.SourceDigest && (initial == nil || current.Disk.SourceDigest != initial.Disk.SourceDigest) {
		return agentsDiskError("a different source was published during conversion; retry the snapshot")
	}
	record, _, readErr := s.readAgentsRecord(root, disk.Content.SHA256)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if readErr == nil {
		if record.Disk.Content != disk.Content {
			return agentsDiskError("the existing agents receipt has different content")
		}
		if err := checkAgentsRecordWitness(root, record); err != nil {
			return err
		}
	} else {
		if reused {
			return agentsDiskError("the verified reused agents disk disappeared before publication")
		}
		if _, err := root.Lstat(disk.Content.SHA256 + ".img"); !errors.Is(err, os.ErrNotExist) {
			return agentsDiskError("an unreceipted agents image exists; it was preserved")
		}
		ref, err := cachefs.CaptureFile(root, temporary, output, 0o444)
		if err != nil {
			return err
		}
		directory, err := cachefs.CaptureRoot(root)
		if err != nil {
			return err
		}
		domain := s.domain
		record = agentsRecordV1{Version: 1, Domain: &domain, Store: s.generation, Directory: directory, Disk: disk, File: ref, Leaf: temporary}
	}
	return s.withPublication(ctx, func() error {
		if readErr != nil {
			if err := writeAgentsRecord(root, record); err != nil {
				// A failed directory commit can still have published the intent.
				// Preserve that exact part for replay; never leave a known receipt
				// pointing at data removed by an ordinary error defer.
				_, _, checkErr := s.readAgentsRecord(root, disk.Content.SHA256)
				*handedOff = !errors.Is(checkErr, os.ErrNotExist)
				return err
			}
			*handedOff = true
		}
		if err := finishAgentsPublication(root, &record); err != nil {
			return err
		}
		domain := s.domain
		source := agentsSourceV1{Version: 1, Domain: &domain, Store: s.generation, Directory: record.Directory, Disk: disk}
		raw, err := json.Marshal(source)
		if err != nil {
			return err
		}
		return replaceReceipt(root, "source.json", raw)
	})
}
