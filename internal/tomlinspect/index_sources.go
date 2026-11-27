package tomlinspect

import (
	"context"
	"io"
)

// MetadataSettings and MetadataStateJSON identify the actual retained TOML
// snapshot and guarded JSON transaction encoding. Closed domain producers
// also bind the actual filename; these roles grant no domain or authority.
const (
	MetadataSettings  uint8 = 1
	MetadataStateJSON uint8 = 2
	// MetadataSandboxJSON is the original spelling of this JSON encoding
	// role. Domain-specific producers still bind their actual transaction.
	MetadataSandboxJSON = MetadataStateJSON
)

// MetadataSource borrows one actual source for a complete inspection. Main
// owns its descriptor and source lifetime; the backend cannot close/write it.
// Check verifies its current original scope, source identity and mode, rather
// than accepting arbitrary ReaderAt implementations as authority.
type MetadataSource interface {
	io.ReaderAt
	Kind() uint8
	Size() int64
	Check(context.Context) error
}

// SourceIndexFactory is the optional broader inspection capability of the
// SAME captured mode owner. Its actual implementation admits only private
// descriptors issued by that owner for the SAME active original scope.
// Inspector.WithSources refuses when this extension is absent; it never
// substitutes AcquireIndex or creates another provider/resource lifetime.
// Existing IndexFactory.AcquireIndex and IndexLease remain unchanged.
type SourceIndexFactory interface {
	AcquireSources(context.Context, []MetadataSource) (IndexLease, error)
}
