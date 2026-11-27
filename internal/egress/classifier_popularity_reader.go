package egress

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress/classify"
)

// PopularitySourceV1 is the actual immutable inherited file, not a path or
// metadata permission. Only its original server can use its bounded reader.
type PopularitySourceV1 struct {
	mu            sync.RWMutex
	file          *os.File
	sessionID     string
	selected      string
	version       popularityFileVersionV1
	owner         *Server
	inheritedBase int
	closed        bool
}

func (*PopularitySourceV1) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

// ValidPopularitySelectionV1 checks bounded bootstrap metadata and opens nothing.
func ValidPopularitySelectionV1(selected string) bool {
	return selected != "" && len(selected) <= 4096 && utf8.ValidString(selected) && !containsPopularityControlV1(selected)
}

func containsPopularityControlV1(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func (s *Server) bindPopularitySourceV1(source *PopularitySourceV1) error {
	if source == nil {
		return nil
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	version, err := popularitySnapshotVersionV1(source.file)
	if source.closed || source.file == nil || source.owner != nil || source.inheritedBase != 0 || !api.ValidSessionID(source.sessionID) || !ValidPopularitySelectionV1(source.selected) || err != nil || version != source.version {
		return popularityErrorV1()
	}
	source.owner = s
	s.classifier.facts.popularity = source
	return nil
}

func (source *PopularitySourceV1) rank(ctx context.Context, s *Server, selected, domain string) classify.RankFact {
	unknown := classify.RankFact{Status: classify.FactUnknown}
	if source == nil || ctx == nil || ctx.Err() != nil {
		return unknown
	}
	if s == nil || s.policyMu.LockContext(ctx) != nil {
		return unknown
	}
	state, current := s.judgeSourceSnapshotLocked(ctx)
	s.policyMu.Unlock()
	if !current {
		return unknown
	}
	source.mu.RLock()
	defer source.mu.RUnlock()
	if source.closed || source.owner != s || source.file == nil || state == nil || state.metadata.SessionID != source.sessionID || selected != source.selected {
		return unknown
	}
	version, err := popularitySnapshotVersionV1(source.file)
	if err != nil || version != source.version {
		return unknown
	}
	return readClassifierRankV1(ctx, source.file, source.version.Size, domain)
}

// Close follows the original owner's physical job join. No lookup reopens
// a replaced path or manufactures a new source after this closes.
func (source *PopularitySourceV1) Close() error {
	if source == nil {
		return nil
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.closed {
		return nil
	}
	source.closed = true
	if source.file != nil {
		return source.file.Close()
	}
	return nil
}

func (facts *classifierFacts) closePopularityV1() {
	if facts.popularity != nil {
		_ = facts.popularity.Close()
	}
}
