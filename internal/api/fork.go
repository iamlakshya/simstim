package api

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

// Fork and rewind use the ordinary authenticated connection. These DTOs are
// bounded selections and display data; none grants storage or boot authority.
const (
	FeatureFork           = "fork"
	FeatureRewind         = "rewind"
	OpFork                = "fork"
	OpCheckpoints         = "checkpoints"
	OpRewind              = "rewind"
	ForkMaxCount          = 32
	CheckpointTimeLayout  = "20060102T150405.000Z"
	CheckpointListMax     = 1024
	CheckpointRefsMax     = 4096
	CheckpointMetadataMax = 1 << 20
)

type ForkArgs struct {
	ID    string `json:"id"`
	Count int    `json:"count"`
	Name  string `json:"name,omitempty"`
}

func (a ForkArgs) Validate() error {
	if !ValidSessionID(a.ID) || a.Count < 1 || a.Count > ForkMaxCount {
		return fmt.Errorf("invalid fork session or count")
	}
	if a.Name != "" {
		if _, _, ok := CheckName(a.Name); !ok || strings.TrimSpace(a.Name) != a.Name {
			return fmt.Errorf("invalid fork name")
		}
	}
	return nil
}

type ForkResult struct {
	Parent   string        `json:"parent"`
	Children []SessionInfo `json:"children"`
	Failures []ForkFailure `json:"failures,omitempty"`
}

// ForkFailure names the one-based requested child whose fresh admission failed.
type ForkFailure struct {
	Index int         `json:"index"`
	Error proto.Error `json:"error"`
}

func (r ForkResult) Validate(a ForkArgs) error {
	if a.Validate() != nil || r.Parent != a.ID || len(r.Children)+len(r.Failures) != a.Count {
		return fmt.Errorf("the fork result differs from its request")
	}
	seen := map[string]bool{a.ID: true}
	for _, child := range r.Children {
		if !ValidSessionID(child.ID) || seen[child.ID] {
			return fmt.Errorf("the fork result repeats or changes a child identity")
		}
		seen[child.ID] = true
	}
	failed := make(map[int]bool, len(r.Failures))
	for _, failure := range r.Failures {
		if failure.Index < 1 || failure.Index > a.Count || failed[failure.Index] || failure.Error.Code == "" || len(failure.Error.Code) > 64 || CleanText(failure.Error.Code, 64) != failure.Error.Code || failure.Error.Message == "" || len(failure.Error.Message) > 4096 || failure.Error.Hint == "" || len(failure.Error.Hint) > 4096 {
			return fmt.Errorf("invalid failed child admission")
		}
		failed[failure.Index] = true
	}
	return checkpointWireBound(r)
}

type CheckpointsArgs struct {
	ID string `json:"id"`
}

func (a CheckpointsArgs) Validate() error {
	if !ValidSessionID(a.ID) {
		return fmt.Errorf("invalid checkpoint session")
	}
	return nil
}

// CheckpointInfo names a committed immutable disk checkpoint. Refs are
// cleaned guest claims and are shown solely to review a rewind.
type CheckpointInfo struct {
	Name      string            `json:"name"`
	CreatedAt time.Time         `json:"created_at"`
	Refs      []proto.BundleRef `json:"refs,omitempty"`
}

func ValidCheckpointName(name string) bool {
	t, err := time.Parse(CheckpointTimeLayout, name)
	return err == nil && t.Format(CheckpointTimeLayout) == name
}

func (c CheckpointInfo) Validate() error {
	if !ValidCheckpointName(c.Name) || c.CreatedAt.IsZero() || c.CreatedAt.UTC().Format(CheckpointTimeLayout) != c.Name {
		return fmt.Errorf("invalid checkpoint identity or time")
	}
	return validateCheckpointRefs(c.Refs)
}

type CheckpointsResult struct {
	ID          string            `json:"id"`
	Checkpoints []CheckpointInfo  `json:"checkpoints"`
	CurrentRefs []proto.BundleRef `json:"current_refs,omitempty"`
}

func (r CheckpointsResult) Validate(id string) error {
	if !ValidSessionID(id) || r.ID != id || r.Checkpoints == nil || len(r.Checkpoints) > CheckpointListMax {
		return fmt.Errorf("invalid checkpoint session or count")
	}
	previous := ""
	for _, checkpoint := range r.Checkpoints {
		if checkpoint.Validate() != nil || previous != "" && checkpoint.Name >= previous {
			return fmt.Errorf("checkpoints are invalid, repeated or not newest first")
		}
		previous = checkpoint.Name
	}
	if err := validateCheckpointRefs(r.CurrentRefs); err != nil {
		return err
	}
	return checkpointWireBound(r)
}

func validateCheckpointRefs(refs []proto.BundleRef) error {
	if len(refs) > CheckpointRefsMax {
		return fmt.Errorf("too many checkpoint branch claims")
	}
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if len(ref.Name) > 1024 || !strings.HasPrefix(ref.Name, "refs/heads/") || len(ref.Name) == len("refs/heads/") || CleanText(ref.Name, 1024) != ref.Name || seen[ref.Name] || !ValidGitOID(ref.OID) {
			return fmt.Errorf("invalid checkpoint branch claim")
		}
		seen[ref.Name] = true
	}
	return nil
}

type RewindArgs struct {
	ID         string `json:"id"`
	Checkpoint string `json:"checkpoint"`
}

func (a RewindArgs) Validate() error {
	if !ValidSessionID(a.ID) || !ValidCheckpointName(a.Checkpoint) {
		return fmt.Errorf("invalid rewind selection")
	}
	return nil
}

type RewindResult struct {
	ID         string      `json:"id"`
	Checkpoint string      `json:"checkpoint"`
	Session    SessionInfo `json:"session"`
}

func (r RewindResult) Validate(a RewindArgs) error {
	if a.Validate() != nil || r.ID != a.ID || r.Session.ID != a.ID || r.Checkpoint != a.Checkpoint {
		return fmt.Errorf("the rewind result differs from its selection")
	}
	return checkpointWireBound(r)
}

func checkpointWireBound(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded)+1 > CheckpointMetadataMax {
		return fmt.Errorf("fork or checkpoint metadata exceeds its frame bound")
	}
	return nil
}

// The exact shared schema barrier applies before the ordinary decoder can
// collapse duplicate selectors, aliases or nested checkpoint names.
func (a *ForkArgs) UnmarshalJSON(raw []byte) error {
	type plain ForkArgs
	var value plain
	if err := decodeForkCheckpointJSON(raw, plain{}, &value); err != nil {
		return err
	}
	*a = ForkArgs(value)
	return nil
}

func (r *ForkResult) UnmarshalJSON(raw []byte) error {
	type plain ForkResult
	var value plain
	if err := decodeForkCheckpointJSON(raw, plain{}, &value); err != nil {
		return err
	}
	*r = ForkResult(value)
	return nil
}

func (a *CheckpointsArgs) UnmarshalJSON(raw []byte) error {
	type plain CheckpointsArgs
	var value plain
	if err := decodeForkCheckpointJSON(raw, plain{}, &value); err != nil {
		return err
	}
	*a = CheckpointsArgs(value)
	return nil
}

func (r *CheckpointsResult) UnmarshalJSON(raw []byte) error {
	type plain CheckpointsResult
	var value plain
	if err := decodeForkCheckpointJSON(raw, plain{}, &value); err != nil {
		return err
	}
	*r = CheckpointsResult(value)
	return nil
}

func (a *RewindArgs) UnmarshalJSON(raw []byte) error {
	type plain RewindArgs
	var value plain
	if err := decodeForkCheckpointJSON(raw, plain{}, &value); err != nil {
		return err
	}
	*a = RewindArgs(value)
	return nil
}

func (r *RewindResult) UnmarshalJSON(raw []byte) error {
	type plain RewindResult
	var value plain
	if err := decodeForkCheckpointJSON(raw, plain{}, &value); err != nil {
		return err
	}
	*r = RewindResult(value)
	return nil
}

func decodeForkCheckpointJSON(raw []byte, shape, target any) error {
	if len(raw)+1 > CheckpointMetadataMax || !ExactJSONFields(raw, shape) {
		return fmt.Errorf("invalid, ambiguous or oversized fork or checkpoint metadata")
	}
	return json.Unmarshal(raw, target)
}

// Clone returns display data that cannot change a retained selection.
func (r CheckpointsResult) Clone() CheckpointsResult {
	r.Checkpoints = slices.Clone(r.Checkpoints)
	for i := range r.Checkpoints {
		r.Checkpoints[i].Refs = slices.Clone(r.Checkpoints[i].Refs)
	}
	r.CurrentRefs = slices.Clone(r.CurrentRefs)
	return r
}
