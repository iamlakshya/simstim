package egress

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

func TestPhysicalH2KeepsExactOldValidTrailersAndRefusesOversizedFinalFragment(t *testing.T) {
	for _, extra := range []int{0, 1} {
		t.Run(strings.Repeat("extra", extra), func(t *testing.T) {
			type result struct {
				value string
				err   error
			}
			handled := make(chan result, 1)
			writer, events := physicalH2NativeFixture(t, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				_, err := io.ReadAll(request.Body)
				handled <- result{request.Trailer.Get("X-Trailer"), err}
				if err == nil {
					w.WriteHeader(http.StatusNoContent)
				}
			}), 65535)
			var block bytes.Buffer
			encoder := hpack.NewEncoder(&block)
			initial := physicalH2Fields(t, encoder, &block, []hpack.HeaderField{{Name: ":method", Value: "POST"}, {Name: ":scheme", Value: "https"}, {Name: ":path", Value: "/original"}, {Name: ":authority", Value: "physical.example.test"}, {Name: "trailer", Value: "x-trailer"}})
			if err := writer.WriteHeaders(http2.HeadersFrameParam{StreamID: 1, BlockFragment: initial, EndHeaders: true}); err != nil {
				t.Fatal(err)
			}
			value := strings.Repeat("a", physicalH2OriginalHeaderLimit-len("x-trailer")-32+extra)
			trailer := physicalH2Fields(t, encoder, &block, []hpack.HeaderField{{Name: "x-trailer", Value: value}})
			middle := len(trailer) / 2
			if err := writer.WriteHeaders(http2.HeadersFrameParam{StreamID: 1, BlockFragment: trailer[:middle], EndHeaders: false, EndStream: true}); err != nil {
				t.Fatal(err)
			}
			if err := writer.WriteContinuation(1, true, trailer[middle:]); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-handled:
				if extra == 0 && (result.err != nil || result.value != value) {
					t.Fatal("exact old-limit trailer changed its original fields or dictionary", result.err)
				}
				if extra != 0 && (result.err == nil || len(result.value) > physicalH2OriginalHeaderLimit-len("x-trailer")-32) {
					t.Fatal("internally enlarged token allowance admitted an oversized guest trailer")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("valid trailer completion or oversized-input refusal did not join its handler")
			}
			if extra == 0 {
				for {
					select {
					case event := <-events:
						if event.err != nil {
							t.Fatal(event.err)
						}
						if event.stream == 1 && event.ended {
							return
						}
					case <-time.After(5 * time.Second):
						t.Fatal("valid old-limit trailer prevented its original response")
					}
				}
			}
		})
	}
}
