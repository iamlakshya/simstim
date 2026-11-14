package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"path"
	"slices"
	"strings"
)

// DefaultCopyMax and DefaultSeedMax are independent per-start byte bounds.
const (
	DefaultCopyMax  int64 = 64 << 20
	DefaultSeedMax  int64 = 512 << 20
	CopyFileMax           = 10000
	SessionInputsV1       = 1
)

// InputFile describes one transferred leaf. It never contains a laptop path.
type InputFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
	SHA256 string `json:"sha256"`
	// Directory is an explicit empty source directory, distinct from an
	// empty regular file. Implicit ancestors do not consume leaf slots.
	Directory bool `json:"directory,omitempty"`
	// Include is a primary repository's explicitly reviewed loose exposure.
	// It is data only. Local source authority is never inferred from this flag.
	Include bool `json:"include,omitempty"`
}

// EmptyInputHash is the exact zero-byte content hash for directory leaves.
const EmptyInputHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// InputRepository describes one laptop bundle cloned under /work/Name.
type InputRepository struct {
	Name         string `json:"name"`
	SourceBranch string `json:"source_branch,omitempty"`
	Commit       string `json:"commit"`
	Origin       string `json:"origin,omitempty"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
	Dirty        bool   `json:"dirty,omitempty"`
	ObjectFormat string `json:"object_format"`
}

// GitIdentity is static laptop Git identity, written only to each clone's config.
type GitIdentity struct {
	// Known reports a successful static laptop read, even when both fields
	// are unset. It is metadata and grants no startup or outbound authority.
	Known bool   `json:"known"`
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

// SessionInputs is an immutable declaration for one nonce-bound input phase.
// The protected session identity and original saved host remain outside it.
// Nonce grants no authority without that existing authenticated request.
type SessionInputs struct {
	Version      int               `json:"version"`
	Nonce        InputNonce        `json:"nonce"`
	Files        []InputFile       `json:"files,omitempty"`
	Repositories []InputRepository `json:"repositories,omitempty"`
	Identity     GitIdentity       `json:"identity"`
}

// Canonical returns an owned declaration whose files have raw-path order.
// Repository order is retained because the first selected source may be the
// primary workspace. All mutable slices are cloned before a publication gate.
func (p SessionInputs) Canonical() SessionInputs {
	p.Files = slices.Clone(p.Files)
	p.Repositories = slices.Clone(p.Repositories)
	slices.SortFunc(p.Files, func(a, b InputFile) int { return strings.Compare(a.Path, b.Path) })
	return p
}

// Validate checks the declaration against already resolved, trusted ceilings.
// A peer cannot raise either ceiling by putting a limit in its declaration.
func (p SessionInputs) Validate(copyMax, seedMax int64) error {
	bad := func(message string) error {
		return fmt.Errorf("%s. Try: prepare the local inputs again before starting the session", message)
	}
	if p.Version != SessionInputsV1 || !p.Nonce.Valid() || copyMax <= 0 || seedMax <= 0 {
		return bad("invalid session input version, nonce or limits")
	}
	if len(p.Files) > CopyFileMax {
		return bad("too many copied files")
	}
	var total int64
	paths := make(map[string]bool, len(p.Files))
	for _, file := range p.Files {
		if !InputGuestPath(file.Path) || file.Mode & ^uint32(0777) != 0 || file.Size < 0 || !inputDigest(file.SHA256) {
			return bad("invalid copied file declaration")
		}
		if file.Directory && (file.Size != 0 || file.SHA256 != EmptyInputHash) {
			return bad("a directory leaf has data or an invalid empty hash")
		}
		if file.Include && (file.Directory || len(p.Repositories) == 0 || !strings.HasPrefix(file.Path, "/work/"+p.Repositories[0].Name+"/")) {
			return bad("an include is not a regular loose file in the primary laptop repository")
		}
		if paths[file.Path] {
			return bad("copied file destinations overlap")
		}
		paths[file.Path] = true
		if file.Size > copyMax-total {
			return bad("copied files exceed copy_max")
		}
		total += file.Size
	}
	for name := range paths {
		for parent := path.Dir(name); parent != "/"; parent = path.Dir(parent) {
			if paths[parent] {
				return bad("copied file destinations overlap")
			}
		}
	}
	seen := make(map[string]bool, len(p.Repositories))
	for _, repo := range p.Repositories {
		if !InputRepoName(repo.Name) || seen[repo.Name] || repo.Size <= 0 || !inputDigest(repo.SHA256) || !ValidGitOID(repo.Commit) {
			return bad("invalid or duplicate input repository")
		}
		if repo.ObjectFormat != "sha1" && repo.ObjectFormat != "sha256" || repo.ObjectFormat == "sha1" && len(repo.Commit) != 40 || repo.ObjectFormat == "sha256" && len(repo.Commit) != 64 {
			return bad("input repository object format does not match its commit")
		}
		if !inputText(repo.SourceBranch) || !inputText(repo.Origin) || repo.Size > seedMax {
			return bad("input repositories exceed seed_max or contain invalid metadata")
		}
		seen[repo.Name] = true
	}
	if !inputText(p.Identity.Name) || !inputText(p.Identity.Email) {
		return bad("invalid laptop Git identity")
	}
	if !p.Identity.Known && (p.Identity.Name != "" || p.Identity.Email != "") || len(p.Repositories) != 0 && !p.Identity.Known {
		return bad("a clone-producing input has no known laptop Git identity")
	}
	return nil
}

// Digest binds the canonical field encoding and order to the publication scope.
// Callers validate first and retain a private cloned declaration after sealing.
func (p SessionInputs) Digest() (string, error) {
	p = p.Canonical()
	summary := p.summary()
	digest, err := NewInputDigest(summary)
	if err != nil {
		return "", err
	}
	for _, file := range p.Files {
		if err = AddInputFileDigest(digest, file); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// InputSummary fits in the existing authenticated New/wake request. Only
// copied-file declarations stream later. Repository facts are trusted solely
// from this original request, never from a guest receipt or progress frame.
type InputSummary struct {
	Version int        `json:"version"`
	Nonce   InputNonce `json:"nonce"`
	Digest  string     `json:"digest"`
	// FileCount counts regular files and explicit empty directory leaves.
	// Implicit ancestors are not additional transferred entries.
	FileCount    int               `json:"file_count"`
	CopyBytes    int64             `json:"copy_bytes"`
	Repositories []InputRepository `json:"repositories,omitempty"`
	Identity     GitIdentity       `json:"identity"`
}

// Equal compares canonical summary facts, treating absent and empty repository
// lists alike. It does not grant authority to the compared peer.
func (s InputSummary) Equal(other InputSummary) bool {
	return s.Version == other.Version && s.Nonce == other.Nonce && s.Digest == other.Digest &&
		s.FileCount == other.FileCount && s.CopyBytes == other.CopyBytes && s.Identity == other.Identity &&
		slices.Equal(s.Repositories, other.Repositories)
}

func (p SessionInputs) summary() InputSummary {
	result := InputSummary{Version: p.Version, Nonce: p.Nonce, FileCount: len(p.Files), Repositories: p.Repositories, Identity: p.Identity}
	for _, file := range p.Files {
		result.CopyBytes += file.Size // caller validates the complete plan first
	}
	return result
}

// Summary seals a fully validated local plan. The ordinary request's ACTUAL
// encoded frame is checked separately before fetching or publishing values.
func (p SessionInputs) Summary(copyMax, seedMax int64) (InputSummary, error) {
	p = p.Canonical()
	if err := p.Validate(copyMax, seedMax); err != nil {
		return InputSummary{}, err
	}
	result := p.summary()
	digest, err := p.Digest()
	result.Digest = digest
	result.Repositories = append([]InputRepository(nil), p.Repositories...)
	return result, err
}

// Validate checks summary bounds without treating its digest as authority for
// copied files. The receiver must validate the full declared set before writes.
func (s InputSummary) Validate(copyMax, seedMax int64) error {
	if !inputDigest(s.Digest) || s.FileCount < 0 || s.FileCount > CopyFileMax || s.CopyBytes < 0 || s.CopyBytes > copyMax || s.FileCount == 0 && s.CopyBytes != 0 {
		return fmt.Errorf("invalid input summary. Try: prepare the local inputs again")
	}
	return (SessionInputs{Version: s.Version, Nonce: s.Nonce, Repositories: s.Repositories, Identity: s.Identity}).Validate(copyMax, seedMax)
}

// NewInputDigest starts the single canonical manifest digest. Digest is
// excluded from its own input. All other exact summary facts are included.
// This pure hash conveys no grant or source authority.
func NewInputDigest(summary InputSummary) (hash.Hash, error) {
	summary.Digest = ""
	data, err := json.Marshal(summary)
	if err != nil {
		return nil, err
	}
	digest := sha256.New()
	digest.Write([]byte("session-inputs-v1\x00"))
	addInputDigestBytes(digest, data)
	return digest, nil
}

// AddInputFileDigest adds one exact declaration in its transmitted order.
func AddInputFileDigest(digest hash.Hash, file InputFile) error {
	data, err := json.Marshal(file)
	if err != nil {
		return err
	}
	addInputDigestBytes(digest, data)
	return nil
}

func addInputDigestBytes(digest hash.Hash, data []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(data)))
	digest.Write(size[:])
	digest.Write(data)
}

func inputDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// InputNonce identifies one input phase. It is never a session ID, pathname
// or grant, and is not reused for another start or declaration digest.
type InputNonce string

// NewInputNonce makes a fresh 256-bit phase identity.
func NewInputNonce() (InputNonce, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("make input nonce: %w", err)
	}
	return InputNonce(hex.EncodeToString(value[:])), nil
}

// Valid reports whether the nonce has the canonical lower-case hex shape.
func (n InputNonce) Valid() bool { return inputDigest(string(n)) }
