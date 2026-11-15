//go:build linux

package microvm

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
	"sort"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/supply"
)

// Boot evidence is host-owned compatibility evidence. It never supplies any
// execution authority: Prepare/Open still require a fresh client plan.
type bootEvidence struct {
	Version int       `json:"version"`
	Key     string    `json:"key"`
	Booted  time.Time `json:"booted"`
}

func evidenceKey(manifest supply.Manifest, facts api.CapProbe, identity string) (string, error) {
	return evidenceKeyFor(cloudHypervisorBackend, manifest, facts, identity)
}

func evidenceKeyFor(kind backendKind, manifest supply.Manifest, facts api.CapProbe, identity string) (string, error) {
	if err := kind.validateRuntime(manifest, facts.Arch); err != nil {
		return "", err
	}
	if facts.Kernel == "" || identity == "" {
		return "", fmt.Errorf("the host kernel identity is missing")
	}
	items := make([]supply.Item, 0, 3)
	for _, selector := range kind.roles() {
		item, ok := manifest.Find(selector.Kind, selector.Name, facts.Arch)
		if !ok {
			return "", fmt.Errorf("the microvm runtime is missing %s", selector.Kind)
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Kind < items[j].Kind })
	data, err := json.Marshal(struct {
		Version    int           `json:"version"`
		Arch       string        `json:"arch"`
		Kernel     string        `json:"kernel"`
		HostHelper string        `json:"host_helper"`
		UIDMode    string        `json:"uid_mode"`
		Items      []supply.Item `json:"items"`
	}{2, facts.Arch, facts.Kernel, identity, uidMode(facts), items})
	if err != nil {
		return "", fmt.Errorf("encode microvm boot identity: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (d *Driver) checkBootEvidence(manifest supply.Manifest, facts api.CapProbe) error {
	identity, err := d.options.Identity()
	if err != nil {
		return err
	}
	key, err := evidenceKeyFor(d.kind, manifest, facts, identity)
	if err != nil {
		return err
	}
	root, err := cachefs.OpenPrivateRoot(d.options.Evidence, false)
	if err != nil {
		return fmt.Errorf("successful microvm boot evidence is unavailable: %w", err)
	}
	defer root.Close()
	file, err := cachefs.OpenFile(root, key+".json", os.O_RDONLY, 0o600)
	if err != nil {
		return fmt.Errorf("this microvm runtime has no successful boot evidence: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 {
		return fmt.Errorf("microvm boot evidence cannot be read within its bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var evidence bootEvidence
	if err := decoder.Decode(&evidence); err != nil {
		return fmt.Errorf("decode microvm boot evidence: %w", err)
	}
	var more any
	if err := decoder.Decode(&more); !errors.Is(err, io.EOF) {
		return fmt.Errorf("microvm boot evidence has trailing content")
	}
	if evidence.Version != 2 || evidence.Key != key || evidence.Booted.IsZero() || evidence.Booted.After(time.Now().Add(time.Minute)) {
		return fmt.Errorf("microvm boot evidence does not match the verified runtime and host")
	}
	return nil
}

func (d *Driver) recordBoot(ctx context.Context, manifest supply.Manifest, facts api.CapProbe) error {
	identity, err := d.options.Identity()
	if err != nil {
		return err
	}
	key, err := evidenceKeyFor(d.kind, manifest, facts, identity)
	if err != nil {
		return err
	}
	root, err := cachefs.OpenPrivateRoot(d.options.Evidence, true)
	if err != nil {
		return err
	}
	defer root.Close()
	data, err := json.Marshal(bootEvidence{Version: 2, Key: key, Booted: time.Now().UTC()})
	if err != nil {
		return fmt.Errorf("encode microvm boot evidence: %w", err)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("name microvm boot evidence: %w", err)
	}
	name := ".boot-" + hex.EncodeToString(nonce[:])
	file, err := cachefs.OpenFile(root, name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(name)
	_, writeErr := file.Write(append(data, '\n'))
	err = errors.Join(writeErr, file.Sync(), file.Close())
	if err != nil {
		return fmt.Errorf("write microvm boot evidence: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := root.Rename(name, key+".json"); err != nil {
		return fmt.Errorf("publish microvm boot evidence: %w", err)
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return cachefs.SyncDirectory(dir)
}
