//go:build linux

package supply

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

const execModeRecordMax = 4096
const execModeRole = "vmm-exec-mode-v1"

// execModeIntent is durable producer intent, NEVER execution or repair
// authority alone. Both require the original native OFD fence and inode.
// The stable lease file is not unlinked at normal final exec.
type execModeIntent struct {
	Version       int               `json:"v"`
	Role          string            `json:"role"`
	Nonce         string            `json:"nonce"`
	SHA256        string            `json:"sha256"`
	FSVerity      string            `json:"fsverity"`
	Arch          string            `json:"arch"`
	Size          int64             `json:"size"`
	Armed         bool              `json:"armed"`
	Self          cachefs.Reference `json:"self"`
	Artifact      cachefs.Reference `json:"artifact"`
	ArtifactsRoot cachefs.Reference `json:"artifacts_root"`
	ModesRoot     cachefs.Reference `json:"modes_root"`
}

func modeLeaseName(item Item) string { return item.SHA256 + ".lease" }

func newExecModeIntent(item Item, artifacts, modes *os.Root, artifact, record *os.File) (execModeIntent, error) {
	var value execModeIntent
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return value, err
	}
	ref, err := cachefs.CaptureFile(artifacts, item.SHA256, artifact, 0o555)
	if err != nil {
		return value, err
	}
	self, err := cachefs.CaptureFile(modes, modeLeaseName(item), record, 0o600)
	if err != nil {
		return value, err
	}
	a, err := cachefs.CaptureRoot(artifacts)
	if err != nil {
		return value, err
	}
	m, err := cachefs.CaptureRoot(modes)
	if err != nil {
		return value, err
	}
	return execModeIntent{Version: 1, Role: execModeRole, Nonce: hex.EncodeToString(nonce[:]), SHA256: item.SHA256, FSVerity: item.FSVerity, Arch: item.Arch, Size: item.Size, Self: self, Artifact: ref, ArtifactsRoot: a, ModesRoot: m}, nil
}

func (value execModeIntent) checkNative(item Item, artifacts, modes *os.Root, metadata, record *os.File) error {
	if err := value.checkRecord(item, artifacts, modes, record); err != nil {
		return err
	}
	info, err := modeArtifactInfo(artifacts, item, metadata)
	if err != nil {
		return err
	}
	return sameModeArtifact(value.Artifact, artifacts, item, metadata, info.Mode())
}

func (value execModeIntent) checkRecord(item Item, artifacts, modes *os.Root, record *os.File) error {
	if value.Version != 1 || value.Role != execModeRole || len(value.Nonce) != 32 || value.SHA256 != item.SHA256 || value.Size != item.Size || value.Arch != item.Arch || !IsHex64(value.FSVerity) || item.FSVerity != "" && value.FSVerity != item.FSVerity {
		return fmt.Errorf("runtime mode intent differs from the original artifact pins")
	}
	if _, err := hex.DecodeString(value.Nonce); err != nil {
		return fmt.Errorf("runtime mode intent nonce is malformed")
	}
	if err := value.ArtifactsRoot.CheckRoot(artifacts); err != nil {
		return err
	}
	if err := value.ModesRoot.CheckRoot(modes); err != nil {
		return err
	}
	if err := value.Self.CheckCreatedFile(modes, modeLeaseName(item), record, 0o600); err != nil {
		return err
	}
	if !value.Artifact.Known() || value.Artifact.Directory || value.Artifact.Mode != 0o555 || value.Artifact.Size != item.Size {
		return fmt.Errorf("runtime mode intent has no original artifact reference")
	}
	return nil
}

func readExecModeIntent(file *os.File) (execModeIntent, error) {
	var value execModeIntent
	info, err := file.Stat()
	if err != nil {
		return value, err
	}
	if info.Size() <= 0 || info.Size() > execModeRecordMax {
		return value, fmt.Errorf("runtime mode intent is missing or too large")
	}
	data, err := io.ReadAll(io.NewSectionReader(file, 0, info.Size()))
	if err != nil {
		return value, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, fmt.Errorf("runtime mode intent is malformed")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return value, fmt.Errorf("runtime mode intent has trailing data")
	}
	return value, nil
}

func writeExecModeIntent(ctx context.Context, value execModeIntent, modes *os.Root, item Item, file *os.File) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := value.Self.CheckCreatedFile(modes, modeLeaseName(item), file, 0o600); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data)+1 > execModeRecordMax {
		return fmt.Errorf("runtime mode intent exceeds its closed bound")
	}
	data = append(data, '\n')
	if _, err := file.WriteAt(data, 0); err != nil {
		return err
	}
	if err := file.Truncate(int64(len(data))); err != nil {
		return err
	}
	return file.Sync()
}
