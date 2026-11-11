package link

import (
	"fmt"
	"io"
	"testing"
)

// BenchmarkThroughput passes bytes through one holder stream of a link over
// a socket pair, for each window from 64 KiB to 1 MiB (spec 5.2 and 12).
// The writer writes 32 KiB at a time, as the holder writes its output
// frames, and the reader reads 64 KiB at a time. Each op passes 1 MiB, so
// this passes 1 GiB for each window, on one core:
//
//	go test ./internal/link -run '^$' -bench Throughput -benchtime 1024x -cpu 1
//
// Every window passes the target of 100 MiB/s by far. DefaultWindow comes
// from the supervisor's memory budget (spec 5.2).
func BenchmarkThroughput(b *testing.B) {
	for _, w := range []uint32{64 << 10, 128 << 10, 256 << 10, 512 << 10, 1 << 20} {
		b.Run(fmt.Sprintf("window=%dKiB", w>>10), func(b *testing.B) {
			sup, guest := pair(b, Options{Window: w})
			ctx := testCtx(b)
			st, err := sup.Open(ctx, KindHolder, nil)
			if err != nil {
				b.Fatal(err)
			}
			g, err := guest.Accept(ctx)
			if err != nil {
				b.Fatal(err)
			}
			done := make(chan int64)
			go func() { done <- drain(g) }()
			chunk := make([]byte, 32<<10)
			b.SetBytes(1 << 20)
			b.ReportAllocs()
			for b.Loop() {
				for range 32 {
					if _, err := st.Write(chunk); err != nil {
						b.Fatal(err)
					}
				}
			}
			_ = st.CloseWrite()
			if n := <-done; n != int64(b.N)<<20 {
				b.Fatalf("read %d bytes, want %d", n, int64(b.N)<<20)
			}
		})
	}
}

// BenchmarkSocketPair passes the same bytes through the bare socket pair,
// for a comparison.
func BenchmarkSocketPair(b *testing.B) {
	a, c := socketPair(b)
	done := make(chan int64)
	go func() { done <- drain(c) }()
	chunk := make([]byte, 32<<10)
	b.SetBytes(1 << 20)
	b.ReportAllocs()
	for b.Loop() {
		for range 32 {
			if _, err := a.Write(chunk); err != nil {
				b.Fatal(err)
			}
		}
	}
	_ = a.Close()
	if n := <-done; n != int64(b.N)<<20 {
		b.Fatalf("read %d bytes, want %d", n, int64(b.N)<<20)
	}
}

// drain reads r to its end, 64 KiB at a time, and counts the bytes.
func drain(r io.Reader) int64 {
	buf := make([]byte, 64<<10)
	var n int64
	for {
		k, err := r.Read(buf)
		n += int64(k)
		if err != nil {
			return n
		}
	}
}
