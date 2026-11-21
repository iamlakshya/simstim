package egress

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"io"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/egress/classify"
)

// Each lookup reads the already held immutable snapshot. ReaderAt keeps
// concurrent requests independent without a shared seek or a host path.
func readClassifierRankV1(ctx context.Context, source io.ReaderAt, size int64, domain string) classify.RankFact {
	unknown := classify.RankFact{Status: classify.FactUnknown}
	if ctx == nil || ctx.Err() != nil || source == nil || size < 0 || size > maxPopularityBytesV1 {
		return unknown
	}
	limited := &io.LimitedReader{R: io.NewSectionReader(source, 0, size), N: maxPopularityBytesV1 + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 1024), 4096)
	var input bytes.Reader
	buffer := bufio.NewReaderSize(&input, 4096)
	decoder := csv.NewReader(buffer)
	decoder.FieldsPerRecord, decoder.TrimLeadingSpace, decoder.ReuseRecord = 2, true, true
	var found uint64
	rows := 0
	for scanner.Scan() {
		if ctx.Err() != nil || rows >= 1000000 {
			return unknown
		}
		rows++
		input.Reset(scanner.Bytes())
		buffer.Reset(&input)
		record, err := decoder.Read()
		if err != nil {
			return unknown
		}
		if rows == 1 && strings.EqualFold(record[0], "rank") && strings.EqualFold(record[1], "domain") {
			continue
		}
		rank, err := strconv.ParseUint(strings.TrimSpace(record[0]), 10, 64)
		if err != nil || rank == 0 {
			return unknown
		}
		host, err := NormaliseHost(strings.TrimSpace(record[1]))
		if err != nil {
			return unknown
		}
		if host == domain {
			if found != 0 && found != rank {
				return unknown
			}
			found = rank
		}
	}
	if scanner.Err() != nil || limited.N == 0 || ctx.Err() != nil || found == 0 {
		return unknown
	}
	return classify.RankFact{Status: classify.FactKnown, Rank: found}
}
