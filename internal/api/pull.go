package api

import (
	"fmt"
	"strings"
)

// FeaturePullBundle offers transport-independent sandbox pull RPCs.
const FeaturePullBundle = "pull.bundle"

// PullMaxDefault is the default compressed bundle cap, in bytes.
const PullMaxDefault int64 = 512 << 20

// PullReadMax is the most that one pull.read response may carry.
const PullReadMax = 1 << 20

// PullExportsMax bounds exports of one existing outer connection.
const PullExportsMax = 4

// Pull operation names use the existing authenticated bridge connection.
const (
	OpPullBegin = "pull.begin"
	OpPullRead  = "pull.read"
	OpPulled    = "pulled"
)

// PullBeginArgs starts or regenerates a connection-owned export.
type PullBeginArgs struct {
	// Token is a caller-generated opaque operation identity, never a path.
	Token    string `json:"token"`
	ID       string `json:"id"`
	Full     bool   `json:"full,omitempty"`
	Previous string `json:"previous,omitempty"`
}

// PullExport describes an immutable capped export, never a guest pathname.
// DefaultRef is an untrusted claim until native bundle verification and a
// namespaced fetch agree with it. SeedHeads comes only from protected data.
type PullExport struct {
	ID         string   `json:"id"`
	Primary    string   `json:"primary"`
	Token      string   `json:"token"`
	Full       bool     `json:"full,omitempty"`
	Empty      bool     `json:"empty,omitempty"`
	Size       int64    `json:"size"`
	SeedHeads  []string `json:"seed_heads,omitempty"`
	DefaultRef string   `json:"default_ref,omitempty"`
}

// PullReadArgs selects an exact offset of this connection's export.
type PullReadArgs struct {
	ID     string `json:"id"`
	Token  string `json:"token"`
	Offset int64  `json:"offset"`
}

// PullChunk is one bounded sequential bundle part.
type PullChunk struct {
	Data []byte `json:"data"`
	Next int64  `json:"next"`
	EOF  bool   `json:"eof,omitempty"`
}

// PulledArgs acknowledges fetched commit IDs or aborts only this export.
// Abort never records commits and still closes every owned transient resource.
type PulledArgs struct {
	ID      string   `json:"id"`
	Token   string   `json:"token"`
	Commits []string `json:"commits,omitempty"`
	Abort   bool     `json:"abort,omitempty"`
}

// PulledResult lists the commit IDs retained in the protected record.
type PulledResult struct {
	Commits []string `json:"commits,omitempty"`
}

// ValidGitOID checks an unambiguous SHA-1 or SHA-256 object ID.
func ValidGitOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// Validate checks a peer's export description before any bytes or Git run.
func (p PullExport) Validate(requestID string, limit int64) error {
	if !ValidSessionID(requestID) || p.ID != requestID || !ValidSessionID(p.Primary) || !ValidSessionID(p.Token) || len(p.Token) != 26 {
		return fmt.Errorf("invalid pull export identity")
	}
	if p.Size < 0 || p.Size > limit || p.Empty && p.Size != 0 || !p.Empty && p.Size == 0 {
		return fmt.Errorf("invalid pull export size")
	}
	for _, head := range p.SeedHeads {
		if !ValidGitOID(head) {
			return fmt.Errorf("invalid trusted seed object ID")
		}
	}
	if strings.ContainsAny(p.DefaultRef, "\x00\r\n") {
		return fmt.Errorf("invalid pull branch claim")
	}
	return nil
}
