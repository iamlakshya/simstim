package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	"github.com/iamlakshya/simstim/internal/proto"
)

const (
	FeatureRewindPreparationV1 = "rewind-preparation-v1"
	OpRewindPrepareV1          = "rewind.prepare-v1"
	OpRewindPreparedCheckV1    = "rewind.prepared.check-v1"
	OpRewindCommitV1           = "rewind.commit-v1"
	OpRewindPreparedCancelV1   = "rewind.prepared.cancel-v1"
)

// PreparedRewindV1 selects only one original preparation on the same accepted
// connection. It is never a native absence, a Resume token or local permission.
type PreparedRewindV1 struct {
	V     int    `json:"v"`
	Token string `json:"token"`
}

func (p *PreparedRewindV1) Valid() bool {
	if p == nil || p.V != 1 || len(p.Token) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(p.Token)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == p.Token
}

func (PreparedRewindV1) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, Redacted) }

func ClonePreparedRewindV1(p *PreparedRewindV1) *PreparedRewindV1 {
	if p == nil {
		return nil
	}
	copy := *p
	return &copy
}

// Start is the actual original client's value-free next-start metadata. The
// server retains its genuine live origin, operation and restart admission.
type RewindPrepareArgsV1 struct {
	V         int        `json:"v"`
	Selection RewindArgs `json:"selection"`
	Floor     string     `json:"floor"`
	Start     ResumeArgs `json:"start"`
}

type RewindPrepareResultV1 struct {
	V           int               `json:"v"`
	Prepared    *PreparedRewindV1 `json:"prepared"`
	Selection   RewindArgs        `json:"selection"`
	Source      *SandboxSnapshot  `json:"source"`
	StartDigest string            `json:"start_digest"`
}

type RewindPreparedArgsV1 struct {
	V         int               `json:"v"`
	Prepared  *PreparedRewindV1 `json:"prepared"`
	Selection RewindArgs        `json:"selection"`
}

// Commit carries fresh values only from the same closed client producer.
// Start.Prepared must remain nil: rewind has its own original source.
type RewindCommitArgsV1 struct {
	V         int               `json:"v"`
	Prepared  *PreparedRewindV1 `json:"prepared"`
	Selection RewindArgs        `json:"selection"`
	Start     ResumeArgs        `json:"start"`
}

func RewindPreparationMetadataV1(selection RewindArgs, start ResumeArgs, floor string) (RewindPrepareArgsV1, error) {
	if selection.Validate() != nil || start.ID != selection.ID {
		return RewindPrepareArgsV1{}, fmt.Errorf("invalid original rewind selection")
	}
	metadata, err := ResumePreparationMetadataV1(start)
	if err != nil {
		return RewindPrepareArgsV1{}, err
	}
	metadata.Layers = ProjectClientImagePolicy(ProjectClientInputSources(metadata.Layers))
	return RewindPrepareArgsV1{V: 1, Selection: selection, Floor: floor, Start: metadata}, nil
}

func RewindPreparationDigestV1(selection RewindArgs, start ResumeArgs, floor string) (string, error) {
	metadata, err := RewindPreparationMetadataV1(selection, start, floor)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(metadata)
	if err != nil || len(encoded)+1 > CheckpointMetadataMax {
		return "", fmt.Errorf("rewind metadata exceeds its bound")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (a RewindPrepareArgsV1) Validate() error {
	if a.V != 1 || a.Selection.Validate() != nil || a.Floor != "none" && a.Floor != "process" && a.Floor != "gvisor" && a.Floor != "microvm" || a.Start.ID != a.Selection.ID || a.Start.Prepared != nil || a.Start.NamedSSHKeys != nil || a.Start.Secrets != nil || a.Start.Members != nil || a.Start.ProtectedTrustedSecretValues != nil || a.Start.SecretDelivery != nil || a.Start.RepositoryPolicy != nil || a.Start.Snapshot == nil {
		return fmt.Errorf("rewind preparation contains invalid or private start data")
	}
	if err := a.Start.Snapshot.Check(a.Selection.ID, CheckpointMetadataMax); err != nil {
		return err
	}
	return checkpointWireBound(a)
}

func (r RewindPrepareResultV1) Validate(selection RewindArgs, digest string, source *SandboxSnapshot) error {
	if r.V != 1 || !r.Prepared.Valid() || r.Selection != selection || r.Source == nil || source == nil || r.StartDigest != digest || len(digest) != 64 {
		return fmt.Errorf("the rewind preparation changed its original selection")
	}
	if err := r.Source.Check(selection.ID, CheckpointMetadataMax); err != nil {
		return err
	}
	left, err := json.Marshal(r.Source)
	if err != nil {
		return err
	}
	right, err := json.Marshal(source)
	if err != nil || string(left) != string(right) {
		return fmt.Errorf("the rewind preparation changed its original cohort")
	}
	return checkpointWireBound(r)
}

func (a RewindPreparedArgsV1) Validate() error {
	if a.V != 1 || !a.Prepared.Valid() || a.Selection.Validate() != nil {
		return fmt.Errorf("invalid original rewind preparation")
	}
	return nil
}

func (a RewindCommitArgsV1) Validate() error {
	if (RewindPreparedArgsV1{V: a.V, Prepared: a.Prepared, Selection: a.Selection}).Validate() != nil || a.Start.ID != a.Selection.ID || a.Start.Prepared != nil || a.Start.Snapshot == nil {
		return fmt.Errorf("invalid original rewind commit")
	}
	if err := a.Start.Snapshot.Check(a.Selection.ID, CheckpointMetadataMax); err != nil {
		return err
	}
	encoded, err := json.Marshal(a)
	if err != nil || len(encoded)+1 > proto.MaxFrame {
		return fmt.Errorf("rewind values exceed their existing frame bound")
	}
	return nil
}

func (a *RewindPrepareArgsV1) UnmarshalJSON(raw []byte) error {
	type plain RewindPrepareArgsV1
	var value plain
	if err := decodeForkCheckpointJSON(raw, plain{}, &value); err != nil {
		return err
	}
	*a = RewindPrepareArgsV1(value)
	return nil
}

func (r *RewindPrepareResultV1) UnmarshalJSON(raw []byte) error {
	type plain RewindPrepareResultV1
	var value plain
	if err := decodeForkCheckpointJSON(raw, plain{}, &value); err != nil {
		return err
	}
	*r = RewindPrepareResultV1(value)
	return nil
}

func (a *RewindPreparedArgsV1) UnmarshalJSON(raw []byte) error {
	type plain RewindPreparedArgsV1
	var value plain
	if err := decodeForkCheckpointJSON(raw, plain{}, &value); err != nil {
		return err
	}
	*a = RewindPreparedArgsV1(value)
	return nil
}

func (a *RewindCommitArgsV1) UnmarshalJSON(raw []byte) error {
	type plain RewindCommitArgsV1
	var value plain
	if len(raw)+1 > proto.MaxFrame || !ExactJSONFields(raw, plain{}) {
		return fmt.Errorf("invalid, ambiguous or oversized rewind values")
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	*a = RewindCommitArgsV1(value)
	return nil
}

func (RewindPrepareResultV1) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, Redacted) }
func (RewindCommitArgsV1) Format(f fmt.State, _ rune)    { _, _ = io.WriteString(f, Redacted) }
