package tomlinspect

// Records contain ONLY encoded source spans and fixed semantic facts. IDs
// and contiguous slot ordinals are relative to their one live StateRegion.
// They carry no decoded names, alphabets, values, commands or permission.
type SemanticEntry struct {
	Kind                                   uint8
	Armed                                  bool
	Name, Sentinel, Identity, Prefix, Rule Span
	BindingID, SlotBase, SlotCount         uint64
}
type BindingRecord struct {
	Name, Header, Format   Span
	HostCount              uint64
	FormatKind             uint8
	UserBytes, HeaderBytes uint64
}
type HostRecord struct {
	BindingID                          uint64
	WholeSpan, InteriorSpan, ValueSpan Span
	ReferenceID                        uint64
}
type SlotRecord struct {
	Kind, Width uint8
	Count       uint64
	ASCIISpan   Span
}

const (
	layoutEntry byte = iota
	layoutBinding
	layoutHost
	layoutSlot
	layoutRun
	layoutFrontier
	layoutBits
	metadataLayoutCount
)

func metadataRecordWidth(layout byte) int {
	switch layout {
	case layoutEntry, layoutBinding:
		return 192
	case layoutHost, layoutSlot, layoutRun:
		return 96
	case layoutFrontier:
		return 64
	default:
		return 0
	}
}
func (i *Inspector) recordLimit(layout byte) uint64 {
	switch layout {
	case layoutEntry:
		return i.outer.stateCounts.entries
	case layoutBinding:
		return i.outer.stateCounts.bindings
	case layoutHost:
		return i.outer.stateCounts.hosts
	case layoutSlot:
		return i.outer.stateCounts.slots
	case layoutRun:
		return i.outer.entryCounts.runs
	case layoutFrontier:
		value, _ := chargeCeil(i.outer.entryCounts.frontierIDs, 8)
		return value
	default:
		return 0
	}
}
func (s *StateRegion) span(span Span) error { return s.h.CheckSpan(s.h.owner.outer.stateSource, span) }
