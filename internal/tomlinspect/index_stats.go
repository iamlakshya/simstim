package tomlinspect

// MetadataStats reports work/resource counters, not a heap or timing result.
// The unchanged whole actor meter must measure those separate quantities.
type MetadataStats struct {
	SourceSize                                                      int64
	SourceAccesses, SourceReads, SourceReadBytes                    uint64
	PageReads, PageWrites, RecordReads, RecordWrites, RecordIOReads uint64
	IndexReadBytes, IndexWrittenBytes, Pages, PeakPins              uint64
	TrieVisits, NumericLookups, Splits                              uint64
	LexerMaximum                                                    int
}

func (i *Inspector) recordStats(size int64) {
	c := i.store.counters
	stats := MetadataStats{SourceSize: size, SourceAccesses: i.source.clock, SourceReads: i.source.reads, SourceReadBytes: i.source.readBytes, PageReads: c.PageReads, PageWrites: c.PageWrites, RecordReads: c.RecordReads, RecordWrites: c.RecordWrites, RecordIOReads: c.RecordIOReads, IndexReadBytes: c.ReadBytes, IndexWrittenBytes: c.WrittenBytes, Pages: c.Pages, PeakPins: c.PeakPins, TrieVisits: c.TrieVisits, NumericLookups: c.NumericLookups, Splits: c.Splits, LexerMaximum: i.stack.maximum}
	i.statsMu.Lock()
	i.lastStats = stats
	i.statsMu.Unlock()
}

// Stats returns the last completed inspection's counters. It does not cache
// any source value, configured name, validation result or permission.
func (i *Inspector) Stats() MetadataStats {
	if i == nil {
		return MetadataStats{}
	}
	i.statsMu.Lock()
	defer i.statsMu.Unlock()
	return i.lastStats
}
