package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestOrderedStringSubsetValidatesBothCompleteArrays(t *testing.T) {
	cases := []struct {
		name, a, b string
		want, fail bool
	}{
		{"empty", `[]`, `[]`, true, false},
		{"empty subset", `[]`, `["A","B"]`, true, false},
		{"identity", `["A","B"]`, `["A","B"]`, true, false},
		{"gaps", `["B","Z"]`, `["A","B","C","Z"]`, true, false},
		{"decoded escapes", `["A","\u005a"]`, `["\u0041","B","Z"]`, true, false},
		{"missing before", `["A"]`, `["B"]`, false, false},
		{"missing after", `["Z"]`, `["B"]`, false, false},
		{"empty superset", `["Z"]`, `[]`, false, false},
		{"duplicate subset", `["A","A"]`, `["A"]`, false, true},
		{"unsorted subset", `["B","A"]`, `["A","B"]`, false, true},
		{"duplicate superset tail", `["A"]`, `["A","Z","Z"]`, false, true},
		{"unsorted superset after mismatch", `["A"]`, `["B","A"]`, false, true},
		{"non-string subset", `[1]`, `[]`, false, true},
		{"non-string superset tail", `[]`, `["A",1]`, false, true},
		{"null subset", `null`, `[]`, false, true},
		{"null superset", `[]`, `null`, false, true},
		{"trailing comma", `[]`, `["A",]`, false, true},
		{"truncated", `["A"`, `["A"]`, false, true},
		{"outside span suffix", `[] `, `[]`, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(tc.a + "\n" + tc.b)
			p, err := NewJSONBytesProjection(t.Context(), raw)
			if err != nil {
				t.Fatal(err)
			}
			got, err := p.OrderedStringArraySubset(JSONSpan{Start: 0, End: int64(len(tc.a))}, JSONSpan{Start: int64(len(tc.a) + 1), End: int64(len(raw))})
			if (err != nil) != tc.fail || err == nil && got != tc.want {
				t.Fatalf("subset=%v error=%v, want=%v fail=%v", got, err, tc.want, tc.fail)
			}
		})
	}
}

type subsetCountReaderAt struct {
	r     io.ReaderAt
	bytes int64
}

func (r *subsetCountReaderAt) ReadAt(p []byte, offset int64) (int, error) {
	n, err := r.r.ReadAt(p, offset)
	r.bytes += int64(n)
	return n, err
}

func TestOrderedStringSubsetDoesNotRescanAllowanceForEveryName(t *testing.T) {
	var left, right strings.Builder
	left.WriteByte('[')
	right.WriteByte('[')
	for n := range 4096 {
		if n != 0 {
			right.WriteByte(',')
		}
		fmt.Fprintf(&right, "\"K%04d\"", n)
		if n%2 == 0 {
			if n != 0 {
				left.WriteByte(',')
			}
			fmt.Fprintf(&left, "\"K%04d\"", n)
		}
	}
	left.WriteByte(']')
	right.WriteByte(']')
	raw := left.String() + "\n" + right.String()
	r := &subsetCountReaderAt{r: strings.NewReader(raw)}
	p, err := NewJSONProjection(t.Context(), r, int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	yes, err := p.OrderedStringArraySubset(JSONSpan{Start: 0, End: int64(left.Len())}, JSONSpan{Start: int64(left.Len() + 1), End: int64(len(raw))})
	if err != nil || !yes {
		t.Fatalf("dense ordered subset=%v, %v", yes, err)
	}
	if r.bytes > 16*int64(len(raw))+65536 {
		t.Fatalf("allowance was repeatedly rescanned: read=%d input=%d", r.bytes, len(raw))
	}
}

func TestOrderedStringSubsetCrossesFileWindowsAndCancels(t *testing.T) {
	long := strings.Repeat("A", 16384)
	a, b := `["`+long+`B"]`, `["`+long+`A","`+long+`B","Z"]`
	raw := a + "\n" + b
	p, err := NewJSONProjection(t.Context(), strings.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	left := JSONSpan{Start: 0, End: int64(len(a))}
	right := JSONSpan{Start: int64(len(a) + 1), End: int64(len(raw))}
	if got, err := p.OrderedStringArraySubset(left, right); err != nil || !got {
		t.Fatalf("window-spanning subset=%v, %v", got, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	p, err = NewJSONProjection(ctx, strings.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := p.OrderedStringArraySubset(left, right); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled subset=%v", err)
	}
}
