package tomlinspect

import (
	"context"
	"io"
	"io/fs"
)

// IndexWorkspaceBytes is the ONE reusable lexer and metadata-page slab for
// an admitted inspection. The mode owner serialises all leases of that slab.
const IndexWorkspaceBytes = 512 << 10

// IndexFile is an already unlinked, owned regular descriptor. It exposes no
// pathname, descriptor number, close operation or source/value storage API.
// The factory verifies ownership and unlinks it before the first metadata write.
type IndexFile interface {
	io.ReaderAt
	io.WriterAt
	Stat() (fs.FileInfo, error)
}

// IndexLease owns one serial inspection's anonymous metadata file and shared
// workspace. The inspector closes it only after it stops using both. Close
// revokes file access, clears the slab and releases serial admission once.
// Filesystem syscall completion is not universally context interruptible.
type IndexLease interface {
	File() IndexFile
	Workspace() []byte
	Close() error
}

// IndexFactory is supplied ONLY by the actual mode resource owner. sourceSize
// is the already bounded snapshot length, not a requested new input limit.
// AcquireIndex honours cancellation and grants exactly one lease at a time.
// There is no ambient path or resource-provider fallback in the inspector.
type IndexFactory interface {
	AcquireIndex(ctx context.Context, sourceSize int64) (IndexLease, error)
}
