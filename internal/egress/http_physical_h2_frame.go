package egress

import (
	"bytes"
	"io"

	"golang.org/x/net/http2"
)

// Change only the final END_HEADERS bit, then append one official, bounded
// CONTINUATION. Original payload, priority, padding and END_STREAM stay exact.
func appendPhysicalH2TokenFinal(raw, token []byte, stream uint32) ([]byte, error) {
	if len(raw) < 9 || len(raw) > physicalH2OriginalFrameLimit+9 {
		return nil, errPhysicalH2Binding
	}
	reader := bytes.NewReader(raw)
	framer := http2.NewFramer(io.Discard, reader)
	framer.SetMaxReadFrameSize(physicalH2OriginalFrameLimit)
	// A standalone final CONTINUATION is validated after its original
	// connection framer has checked ordering. Public framer parsing still
	// validates its frame representation and length here.
	framer.AllowIllegalReads = true
	frame, err := framer.ReadFrame()
	if err != nil || reader.Len() != 0 || frame.Header().StreamID != stream || !frame.Header().Flags.Has(http2.FlagHeadersEndHeaders) {
		return nil, errPhysicalH2Binding
	}
	switch frame.(type) {
	case *http2.HeadersFrame, *http2.ContinuationFrame:
	default:
		return nil, errPhysicalH2Binding
	}
	result := bytes.NewBuffer(make([]byte, 0, len(raw)+len(token)+9))
	original := bytes.Clone(raw)
	original[4] &^= byte(http2.FlagHeadersEndHeaders)
	_, _ = result.Write(original)
	writer := http2.NewFramer(result, nil)
	if err := writer.WriteContinuation(stream, true, token); err != nil {
		return nil, errPhysicalH2Binding
	}
	return result.Bytes(), nil
}
