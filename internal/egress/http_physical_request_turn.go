package egress

import "math"

// This is selected by the original owned native origin factory before any
// request bytes leave. The same turn then records only body-byte prefixes
// accepted by the actual, already verified TLS connection.
func (t *ModelUsageTurnV1) beginPhysicalRequestV1() error {
	if t == nil {
		return errPhysicalH2Binding
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished || t.physicalRequest {
		return errPhysicalH2Binding
	}
	t.physicalRequest = true
	t.requestBytes = 0
	return nil
}

func (t *ModelUsageTurnV1) physicalRequestPrefixV1(size uint64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished || t.ignored || !t.physicalRequest {
		return
	}
	if size > math.MaxUint64-t.requestBytes {
		t.requestBytes, t.byteOverflow = math.MaxUint64, true
	} else {
		t.requestBytes += size
	}
}
