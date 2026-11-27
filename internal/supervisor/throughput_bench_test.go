//go:build !windows

package supervisor

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

// BenchmarkRelayThroughput passes actual holder output through the link,
// supervisor frame reader/relay and client writer over real socket pairs.
// One operation is 1 MiB: -benchtime=1024x measures 1 GiB on -cpu=1.
func BenchmarkRelayThroughput(b *testing.B) {
	a, z := benchmarkPairV1(b)
	sup, guest := link.New(a, link.Supervisor, link.Options{}), link.New(z, link.Guest, link.Options{})
	ctx, cancel := context.WithTimeout(b.Context(), 5*time.Minute)
	b.Cleanup(cancel)
	b.Cleanup(func() { _ = sup.Close(); _ = guest.Close() })
	stream, err := sup.Open(ctx, link.KindHolder, nil)
	if err != nil {
		b.Fatal(err)
	}
	other, err := guest.Accept(ctx)
	if err != nil {
		b.Fatal(err)
	}
	write, read := benchmarkPairV1(b)
	s := &Supervisor{m: &host.Manifest{}, link: sup, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	c := &client{conn: write, w: proto.NewWriter(write), done: make(chan struct{})}
	output := make(chan int64, 1)
	go func() {
		reader := bufio.NewReader(read)
		var total int64
		for {
			frame, err := proto.Read(reader)
			if err != nil {
				output <- total
				return
			}
			if frame.Type == proto.TOutput {
				total += int64(len(frame.Payload))
			}
		}
	}()
	batches := make(chan struct{})
	wrote := make(chan error, 1)
	var once sync.Once
	b.Cleanup(func() { once.Do(func() { close(batches) }); _ = write.Close(); _ = read.Close() })
	go func() {
		writer := proto.NewWriter(other)
		chunk := make([]byte, 32<<10)
		for range batches {
			var err error
			for range 32 {
				if err = writer.Write(proto.TOutput, chunk); err != nil {
					break
				}
			}
			wrote <- err
			if err != nil {
				return
			}
		}
	}()
	reader := bufio.NewReader(stream)
	var small []byte
	b.SetBytes(1 << 20)
	b.ReportAllocs()
	for b.Loop() {
		batches <- struct{}{}
		for range 32 {
			typ, size, err := head(reader)
			if err != nil || typ != proto.TOutput {
				b.Fatalf("actual output frame: %v, %v", typ, err)
			}
			var gone, stopped bool
			small, gone, stopped = s.relayBody(c, stream, reader, typ, size, small, time.Now().Add(frameStall), frameStall)
			if gone || stopped {
				b.Fatal("the actual output relay stopped")
			}
		}
		if err := <-wrote; err != nil {
			b.Fatal(err)
		}
	}
	once.Do(func() { close(batches) })
	_ = write.Close()
	if got := <-output; got != int64(b.N)<<20 {
		b.Fatalf("relayed %d bytes, want %d", got, int64(b.N)<<20)
	}
}

func benchmarkPairV1(b *testing.B) (net.Conn, net.Conn) {
	b.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		b.Fatal(err)
	}
	files := []*os.File{os.NewFile(uintptr(fds[0]), "conformance-pair"), os.NewFile(uintptr(fds[1]), "conformance-pair")}
	for _, fd := range fds {
		unix.CloseOnExec(fd)
	}
	a, first := net.FileConn(files[0])
	z, second := net.FileConn(files[1])
	_ = files[0].Close()
	_ = files[1].Close()
	if first != nil || second != nil {
		if a != nil {
			_ = a.Close()
		}
		if z != nil {
			_ = z.Close()
		}
		b.Fatalf("socket pair: %v, %v", first, second)
	}
	b.Cleanup(func() { _ = a.Close(); _ = z.Close() })
	return a, z
}
