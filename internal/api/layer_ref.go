package api

import (
	"unicode"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/proto"
)

// LayerRank returns a layer's broadest-first rank, or -1 for an unknown kind.
// The merger and source-pin codec share this one structural kind table.
func LayerRank(kind LayerKind) int {
	switch kind {
	case LayerBuiltin:
		return 0
	case LayerAll:
		return 1
	case LayerHost:
		return 2
	case LayerRepo:
		return 3
	case LayerProfile:
		return 4
	case LayerSession:
		return 5
	case LayerLockdown:
		return 6
	default:
		return -1
	}
}

// ValidLayerRef reports whether a typed reference can name a policy source.
func ValidLayerRef(ref LayerRef) bool {
	if LayerRank(ref.Kind) < 0 || ref.Source != "" && (ref.Source != SourceRepositoryFile || ref.Kind != LayerRepo) || !utf8.ValidString(ref.Name) || len(ref.Name)+1 > proto.MaxFrame {
		return false
	}
	for _, r := range ref.Name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// ValidSelectedLayerRef applies the same structural contract to a laptop's
// selected sources. Anonymous host/repo/profile layers are a legacy merge
// representation, never a locally selected config identity.
func ValidSelectedLayerRef(ref LayerRef) bool {
	if !ValidLayerRef(ref) || ref.Source != "" {
		return false
	}
	switch ref.Kind {
	case LayerAll, LayerSession:
		return ref.Name == ""
	case LayerHost, LayerRepo, LayerProfile:
		return ref.Name != ""
	default:
		return false
	}
}
