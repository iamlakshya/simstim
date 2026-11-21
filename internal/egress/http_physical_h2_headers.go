package egress

import (
	"strings"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

// The pinned standard server adds ten RFC field overheads to MaxHeaderBytes.
// Preserve that guest ceiling independently of the fixed synthetic overhead.
const physicalH2OriginalHeaderLimit = maxHTTPHeaderBytes + 10*32
const physicalH2OriginalFrameLimit = 1 << 20
const physicalH2OriginalTableLimit = 4096

// One official HPACK decoder follows the original connection dictionary over
// requests and trailers. Only one original fragment and bounded decoder state
// are needed; earlier CONTINUATION fragments can pass through unchanged.
type physicalH2HeaderGuard struct {
	decoder         *hpack.Decoder
	stream          uint32
	remaining       uint32
	trailerRemain   uint32
	truncated       bool
	open            bool
	invalidFragment bool
}

func newPhysicalH2HeaderGuard() *physicalH2HeaderGuard {
	guard := new(physicalH2HeaderGuard)
	guard.decoder = hpack.NewDecoder(physicalH2OriginalTableLimit, guard.field)
	guard.decoder.SetAllowedMaxDynamicTableSize(physicalH2OriginalTableLimit)
	guard.decoder.SetMaxStringLength(physicalH2OriginalHeaderLimit)
	return guard
}

func (g *physicalH2HeaderGuard) begin(stream uint32) error {
	if g.open || stream == 0 || stream&1 == 0 {
		return http2.ConnectionError(http2.ErrCodeProtocol)
	}
	g.stream, g.open = stream, true
	g.remaining, g.trailerRemain = physicalH2OriginalHeaderLimit, physicalH2OriginalHeaderLimit
	g.truncated, g.invalidFragment = false, false
	g.decoder.SetEmitEnabled(true)
	return nil
}

func (g *physicalH2HeaderGuard) field(field hpack.HeaderField) {
	remaining, size := &g.remaining, field.Size()
	if field.Name == "trailer" {
		remaining = &g.trailerRemain
		fields := strings.Count(field.Value, ",") + 1
		size = uint32(len(field.Value)-fields+1) + uint32(fields)*32
	}
	if size > *remaining {
		g.remaining, g.trailerRemain = 0, 0
		g.truncated = true
		g.decoder.SetEmitEnabled(false)
		return
	}
	*remaining -= size
}

// The encoded-fragment bound is the original official framer's guard. It is
// applied before every original fragment, without retaining a whole chain.
func (g *physicalH2HeaderGuard) fragment(stream uint32, fragment []byte, end bool) error {
	if !g.open || stream != g.stream || g.invalidFragment || int64(len(fragment)) > 2*(int64(g.remaining)+int64(g.trailerRemain)) {
		return http2.ConnectionError(http2.ErrCodeProtocol)
	}
	if _, err := g.decoder.Write(fragment); err != nil {
		g.invalidFragment = true
		return http2.ConnectionError(http2.ErrCodeCompression)
	}
	if end {
		g.open = false
		if err := g.decoder.Close(); err != nil {
			return http2.ConnectionError(http2.ErrCodeCompression)
		}
	}
	return nil
}
