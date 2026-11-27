// Package wall defines the wall around one session's sandbox.
package wall

import (
	"context"
	"net"
	"time"

	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/supply"
)

// Driver builds walls of one level on this host.
type Driver interface {
	Name() string
	Level() Level
	Probe(ctx context.Context) Probe
	Prepare(ctx context.Context, spec Spec) (Sandbox, error)
	Open(ctx context.Context, sessionDir string) (Sandbox, error)
}

// Sandbox is one session's sandbox.
type Sandbox interface {
	Start(ctx context.Context) (net.Conn, error)
	Stop(ctx context.Context, grace time.Duration) error
	Kill() error
	Wait() Exit
	Destroy() error
	Abilities() Abilities
}

// Abilities are optional abilities of a sandbox.
type Abilities struct {
	Freeze  bool
	Reflink bool
}

// Exit says how a sandbox ended.
type Exit struct {
	Code   int
	Signal string
	Err    error
}

// RootMode says whether a sandbox keeps changes to its system files.
type RootMode string

// Reset clears system changes at each start. Keep preserves them.
const (
	Reset RootMode = "reset"
	Keep  RootMode = "keep"
)

// ImageRef names the immutable image content used by a sandbox.
type ImageRef struct {
	Digest     string
	RootHash   string
	DataBlocks int64
}

// Spec describes the sandbox to prepare.
type Spec struct {
	// Driver is plan data copied from the validated original Need. It does
	// not authorise execution or select bytes from the host's inventory.
	Driver string
	// RuntimeManifest is cloned plan data. The original native boot owner
	// separately supplies execution authority and held role selection.
	RuntimeManifest *supply.Manifest
	VM              *VMSpec
	ID              string
	SessionDir      string
	Image           *ImageRef
	Mounts          []Mount
	// StateDirs are validated directory-backed runtime paths under /home/agent.
	// The agent table's public list includes both folders and files.
	StateDirs []string
	// StateFiles are validated home-relative file families, copied at clean stops.
	StateFiles []string
	// StateRestore is a committed generation, bound read-only for this run.
	StateRestore string
	// StateCapture is a fresh run-owned incoming area, never a trusted parent.
	StateCapture string
	MemoryMax    int64
	CPUs         int
	DiskMax      int64
	Root         RootMode
	// Helper names the directory containing the host helper and launcher.
	Helper string
}

// Mount is the canonical files.Mount grant record.
type Mount = files.Mount
