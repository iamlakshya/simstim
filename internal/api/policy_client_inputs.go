package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

// PolicyClientInputsV1 identifies the local source-expression projection.
const PolicyClientInputsV1 = 1

// PolicyClientInputsPin records approved LOCAL copy expressions and their
// original working directory. It contains no copied-file history or values,
// grants no filesystem access itself, and must never enter an outgoing RPC.
// A missing pin is unknown. A present empty CopyIn is known empty.
type PolicyClientInputsPin struct {
	Version int      `json:"version"`
	CopyIn  []string `json:"copy_in"`
	CWD     string   `json:"cwd"`
	CopyMax int64    `json:"copy_max"`
	SeedMax int64    `json:"seed_max"`
	Digest  string   `json:"digest"`
}

// NewPolicyClientInputsPin owns a projection of one already approved local
// snapshot. It preserves expression order, tilde and relative spelling. The
// canonical input planner owns expression parsing; this is its data carrier.
func NewPolicyClientInputsPin(copyIn []string, cwd string, copyMax, seedMax int64) (*PolicyClientInputsPin, error) {
	p := &PolicyClientInputsPin{Version: PolicyClientInputsV1, CopyIn: append([]string{}, copyIn...), CWD: cwd, CopyMax: copyMax, SeedMax: seedMax}
	digest, err := p.sourceDigest()
	if err != nil {
		return nil, err
	}
	p.Digest = digest
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return p, nil
}

// Clone owns all mutable local selection data. It creates no authority.
func (p *PolicyClientInputsPin) Clone() *PolicyClientInputsPin {
	if p == nil {
		return nil
	}
	copy := *p
	copy.CopyIn = slices.Clone(p.CopyIn)
	return &copy
}

// Validate checks the known local format and its exact source projection.
// Unknown versions or absent required fields never become an empty plan.
func (p *PolicyClientInputsPin) Validate() error {
	if p == nil || p.Version != PolicyClientInputsV1 || p.CopyIn == nil || !filepath.IsAbs(p.CWD) || !utf8.ValidString(p.CWD) || strings.ContainsRune(p.CWD, 0) || p.CopyMax <= 0 || p.SeedMax <= 0 || p.CopyMax >= 1<<62 || p.SeedMax >= 1<<62 {
		return fmt.Errorf("unsupported or invalid local input source pin")
	}
	for _, expression := range p.CopyIn {
		if expression == "" || !utf8.ValidString(expression) || strings.ContainsRune(expression, 0) {
			return fmt.Errorf("invalid local input source expression")
		}
	}
	digest, err := p.sourceDigest()
	if err != nil {
		return err
	}
	if p.Digest != digest {
		return fmt.Errorf("local input source projection digest changed")
	}
	return nil
}

func (p *PolicyClientInputsPin) sourceDigest() (string, error) {
	// The domain and canonical typed fields identify THIS local projection.
	// They never replace an original repository/source/proposal file hash.
	data := struct {
		Version int      `json:"version"`
		CopyIn  []string `json:"copy_in"`
		CWD     string   `json:"cwd"`
		CopyMax int64    `json:"copy_max"`
		SeedMax int64    `json:"seed_max"`
	}{p.Version, p.CopyIn, p.CWD, p.CopyMax, p.SeedMax}
	raw, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("encode local input source projection: %w", err)
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("policy-client-inputs-v1\x00"))
	_, _ = hash.Write(raw)
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// PinPolicySourcesWithClientInputs attaches the known local input projection
// to the SAME creation pin. It cannot manufacture a pin for legacy nil layers.
func PinPolicySourcesWithClientInputs(layers []Layer, resources PolicyResourcePin, inputs *PolicyClientInputsPin) (*PolicySourcePin, error) {
	pin, err := PinPolicySourcesWithResources(layers, resources)
	if err != nil {
		return nil, err
	}
	if pin == nil {
		if inputs != nil {
			return nil, fmt.Errorf("local inputs need the original local creation snapshot")
		}
		return nil, nil
	}
	pin.ClientInputs = inputs.Clone()
	if err := validatePolicyPin(pin); err != nil {
		return nil, err
	}
	return pin, nil
}
