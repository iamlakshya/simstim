package egress

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/egress/classify"
)

func TestPopularityHeldReaderPreservesStrictCSVAndUnknowns(t *testing.T) {
	for _, test := range []struct {
		data string
		want uint64
	}{
		{"rank,domain\n1,other.example\n12,example.com\n", 12},
		{"\"rank\",\"domain\"\n\"12\",\"example.com\"\n", 12},
		{"12,example.com\n12,example.com\n", 12},
		{"12,example.com\n13,example.com\n", 0},
		{"0,example.com\n", 0},
		{"12,example.com,extra\n", 0},
		{"12,\"example.com\n", 0},
		{"12,example.com\nnot-a-rank,other.example\n", 0},
		{"12,missing.example\n", 0},
		{strings.Repeat("x", 4097), 0},
	} {
		actual := readClassifierRankV1(t.Context(), strings.NewReader(test.data), int64(len(test.data)), "example.com")
		if test.want == 0 {
			if actual.Status != classify.FactUnknown || actual.Rank != 0 {
				t.Fatal("invalid or missing evidence became a popularity rank")
			}
		} else if actual.Status != classify.FactKnown || actual.Rank != test.want {
			t.Fatal("held-reader CSV contract changed")
		}
	}
}

func TestPopularityReaderRetainsOriginalByteRowAndCancellationBounds(t *testing.T) {
	data := []byte("1,example.com\n")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, call := range []func() classify.RankFact{
		func() classify.RankFact {
			return readClassifierRankV1(ctx, bytes.NewReader(data), int64(len(data)), "example.com")
		},
		func() classify.RankFact {
			return readClassifierRankV1(t.Context(), bytes.NewReader(data), maxPopularityBytesV1+1, "example.com")
		},
		func() classify.RankFact {
			large := strings.Repeat("1,other.example\n", 1000000) + "2,example.com\n"
			return readClassifierRankV1(t.Context(), strings.NewReader(large), int64(len(large)), "example.com")
		},
	} {
		if actual := call(); actual.Status != classify.FactUnknown || actual.Rank != 0 {
			t.Fatal("oversized, cancelled or excess-row input supplied a fact")
		}
	}
}

func TestPopularityMarkerCannotManufactureOrWidenDescriptorLayout(t *testing.T) {
	for _, raw := range []string{"", "--popularity-fds-v2=4", "--popularity-fds-v1=3", "--popularity-fds-v1=65", "--popularity-fds-v1=04", "--popularity-fds-v1=-4", "--popularity-fds-v1=4/other"} {
		if _, ok := ParsePopularityFDArgumentV1(raw); ok {
			t.Fatal("invalid internal popularity descriptor layout was accepted")
		}
	}
	if base, ok := ParsePopularityFDArgumentV1("--popularity-fds-v1=4"); !ok || base != 4 {
		t.Fatal("original versioned inherited layout changed")
	}
	server, _ := judgeCapabilityFixture(t)
	if err := server.bindPopularitySourceV1(&PopularitySourceV1{}); err == nil {
		t.Fatal("a metadata-only source issued a native popularity reader")
	}
}
