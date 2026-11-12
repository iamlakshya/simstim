package link

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// pair starts both ends of a link over a socket pair.
func pair(tb testing.TB, opt Options) (sup, guest *Session) {
	tb.Helper()
	a, b := socketPair(tb)
	sup, guest = New(a, Supervisor, opt), New(b, Guest, opt)
	tb.Cleanup(func() { sup.Close(); guest.Close() })
	return sup, guest
}

func testCtx(tb testing.TB) context.Context {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	tb.Cleanup(cancel)
	return ctx
}

// A put stream as spec 10.2 has it: a header, the bytes, a close of the
// writes, one answer, then a close both ways.
func TestOpenAcceptAndHalfClose(t *testing.T) {
	ctx := testCtx(t)
	sup, guest := pair(t, Options{})
	type put struct {
		Name string `json:"name"`
		Size int    `json:"size"`
	}
	st, err := sup.Open(ctx, KindPut, put{Name: "shot.png", Size: 5})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := st.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	g, err := guest.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var h struct {
		put
		Kind Kind `json:"kind"`
	}
	if err := g.Header(&h); err != nil || h.Kind != KindPut || h.Name != "shot.png" || h.Size != 5 || g.Kind() != KindPut {
		t.Fatalf("header %+v, kind %s, %v", h, g.Kind(), err)
	}
	got, err := io.ReadAll(g)
	if err != nil || string(got) != "hello" {
		t.Fatalf("read %q, %v", got, err)
	}
	// The guest still writes after the supervisor's half-close.
	if _, err := g.Write([]byte(`{"path":"/p/shot.png"}`)); err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	got, err = io.ReadAll(st)
	if err != nil || string(got) != `{"path":"/p/shot.png"}` {
		t.Fatalf("answer %q, %v", got, err)
	}
	if _, err := st.Write([]byte("x")); !errors.Is(err, net.ErrClosed) {
		t.Errorf("write after CloseWrite: %v", err)
	}
	// The guest closed both ways.
	waitFor(t, func() bool { st.s.mu.Lock(); defer st.s.mu.Unlock(); return st.peerGone })
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Read(make([]byte, 1)); !errors.Is(err, net.ErrClosed) {
		t.Errorf("read after Close: %v", err)
	}
	waitFor(t, func() bool { return streams(sup) == 0 && streams(guest) == 0 })
}

func streams(s *Session) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.streams)
}

func waitFor(tb testing.TB, ok func() bool) {
	tb.Helper()
	for range 500 {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	tb.Fatal("timed out")
}

// Both sides open at once: the supervisor's IDs are odd, the guest's even.
func TestBothSidesOpen(t *testing.T) {
	ctx := testCtx(t)
	sup, guest := pair(t, Options{})
	a, err := sup.Open(ctx, KindHolder, map[string]string{"session": "s1"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := guest.Open(ctx, KindEgress, map[string]any{"via": "explicit", "host": "example.com", "port": 443})
	if err != nil {
		t.Fatal(err)
	}
	if a.id%2 != 1 || b.id%2 != 0 {
		t.Errorf("IDs %d and %d", a.id, b.id)
	}
	ga, err := guest.Accept(ctx)
	if err != nil || ga.Kind() != KindHolder {
		t.Fatalf("guest accepted %v, %v", ga, err)
	}
	sb, err := sup.Accept(ctx)
	if err != nil || sb.Kind() != KindEgress {
		t.Fatalf("supervisor accepted %v, %v", sb, err)
	}
	var h struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	}
	if err := sb.Header(&h); err != nil || h.Host != "example.com" || h.Port != 443 {
		t.Fatalf("egress header %+v, %v", h, err)
	}
	if err := sb.Header(new(int)); err == nil || !strings.Contains(err.Error(), "egress") {
		t.Errorf("a header into an int: %v", err)
	}
	if got, want := a.LocalAddr().String(), "supervisor/holder/1"; got != want {
		t.Errorf("local address %q, want %q", got, want)
	}
	if got, want := ga.RemoteAddr().String(), "supervisor/holder/1"; got != want || ga.LocalAddr().Network() != "link" {
		t.Errorf("remote address %q, want %q", got, want)
	}
}

// Acceptance: 1,000 streams of mixed sizes complete over a socket pair,
// with the default limits. Each side opens some, every kind that it may
// open but ctl, and the other side answers each with the SHA-256 of what it
// read.
func TestThousandStreams(t *testing.T) {
	ctx := testCtx(t)
	sup, guest := pair(t, Options{})
	for _, s := range []*Session{sup, guest} {
		go serveDigests(ctx, s)
	}
	var supKinds, guestKinds []Kind
	for k, who := range Opener {
		switch {
		case k == KindCtl:
		case who == Supervisor:
			supKinds = append(supKinds, k)
		default:
			guestKinds = append(guestKinds, k)
		}
	}
	// Each side keeps as many streams open as its limit lets it. A slot is
	// free again when Close returns.
	slots := map[*Session]chan struct{}{
		sup:   make(chan struct{}, DefaultMaxSupervisorStreams),
		guest: make(chan struct{}, DefaultMaxGuestStreams),
	}
	const n = 1000
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		from, kinds := sup, supKinds
		if i%4 == 3 {
			from, kinds = guest, guestKinds
		}
		slots[from] <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots[from] }()
			errs <- roundTrip(ctx, from, kinds[i/4%len(kinds)], i)
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, func() bool { return streams(sup) == 0 && streams(guest) == 0 })
	if sup.Refused() != 0 || guest.Refused() != 0 {
		t.Errorf("refused %d and %d", sup.Refused(), guest.Refused())
	}
}

// size gives stream i a size: some of 1 MiB, some of 96 KiB, the rest up
// to 20 KiB, and a few empty.
func size(i int) int {
	switch {
	case i%100 == 0:
		return 1 << 20
	case i%10 == 0:
		return 96 << 10
	case i%25 == 1:
		return 0
	}
	return i * 37 % 20000
}

func roundTrip(ctx context.Context, s *Session, k Kind, i int) (err error) {
	st, err := s.Open(ctx, k, map[string]int{"i": i})
	if err != nil {
		return err
	}
	defer func() {
		if cerr := st.Close(); err == nil {
			err = cerr
		}
	}()
	data := make([]byte, size(i))
	_, _ = rand.NewChaCha8([32]byte{byte(i), byte(i >> 8)}).Read(data)
	if _, err := st.Write(data); err != nil {
		return err
	}
	if err := st.CloseWrite(); err != nil {
		return err
	}
	got, err := io.ReadAll(st)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	if string(got) != hex.EncodeToString(sum[:]) {
		return errors.New("stream " + st.LocalAddr().String() + ": wrong digest")
	}
	return nil
}

// serveDigests answers each stream that the other side opens with the
// SHA-256 of its bytes.
func serveDigests(ctx context.Context, s *Session) {
	for {
		st, err := s.Accept(ctx)
		if err != nil {
			return
		}
		go func() {
			defer st.Close()
			h := sha256.New()
			if _, err := io.Copy(h, st); err != nil {
				return
			}
			_, _ = st.Write([]byte(hex.EncodeToString(h.Sum(nil))))
		}()
	}
}

// Acceptance: a blocked stream does not hold others back. A stream whose
// reader never reads stops its writer when the credit runs out, and the
// link goes on carrying another stream both ways.
func TestBlockedStreamHoldsNoOtherBack(t *testing.T) {
	ctx := testCtx(t)
	const window = 64 << 10
	sup, guest := pair(t, Options{Window: window})
	stuck, err := sup.Open(ctx, KindPort, map[string]int{"port": 3000})
	if err != nil {
		t.Fatal(err)
	}
	gStuck, err := guest.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	up, err := guest.Open(ctx, KindEgress, nil)
	if err != nil {
		t.Fatal(err)
	}
	sUp, err := sup.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wrote := make(chan int, 2)
	for _, st := range []*Stream{stuck, up} {
		go func() {
			n, _ := st.Write(make([]byte, 1<<20))
			wrote <- n
		}()
	}
	h, err := sup.Open(ctx, KindHolder, nil)
	if err != nil {
		t.Fatal(err)
	}
	gh, err := guest.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range [][2]*Stream{{h, gh}, {gh, h}} {
		want := bytes.Repeat([]byte("holder output "), 600_000) // about 8 MiB
		go func() { _, _ = d[0].Write(want) }()
		got := make([]byte, len(want))
		if _, err := io.ReadFull(d[1], got); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("holder stream: %v", err)
		}
	}
	select {
	case n := <-wrote:
		t.Fatalf("a write of 1 MiB to a stream that nobody reads ended after %d bytes", n)
	default:
	}
	for _, st := range []*Stream{stuck, up} {
		st.s.mu.Lock()
		cred, queued := st.sendCred, st.out.len()
		st.s.mu.Unlock()
		if cred != 0 || queued != window {
			t.Errorf("%s: credit %d and %d bytes queued, want 0 and %d", st.LocalAddr(), cred, queued, window)
		}
	}
	// Reading the stuck streams lets their writes finish.
	for _, st := range []*Stream{gStuck, sUp} {
		go func() { _, _ = io.Copy(io.Discard, st) }()
	}
	for range 2 {
		if n := <-wrote; n != 1<<20 {
			t.Errorf("wrote %d bytes", n)
		}
	}
}

// Open refuses what the other side would refuse, before it sends anything.
func TestOpenChecks(t *testing.T) {
	ctx := testCtx(t)
	sup, guest := pair(t, Options{MaxSupervisorStreams: 3, MaxHeader: 64})
	for _, tc := range []struct {
		s      *Session
		k      Kind
		header any
		want   string
	}{
		{sup, KindEgress, nil, "may not open"},
		{guest, KindHolder, nil, "may not open"},
		{sup, "nope", nil, "may not open"},
		{sup, KindPut, map[string]string{"kind": "x"}, `field "kind"`},
		{sup, KindPut, []int{1}, "not a JSON object"},
		{sup, KindPut, func() {}, "encode the header"},
		{sup, KindPut, map[string]string{"name": strings.Repeat("a", 64)}, "more than the 64"},
	} {
		if _, err := tc.s.Open(ctx, tc.k, tc.header); err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "Try:") {
			t.Errorf("open %s with %v: %v, want %q", tc.k, tc.header, err, tc.want)
		}
	}
	// A nil map is an empty header.
	var none map[string]string
	if _, err := sup.Open(ctx, KindCtl, none); err != nil {
		t.Fatal(err)
	}
	if _, err := sup.Open(ctx, KindCtl, nil); err == nil || !strings.Contains(err.Error(), "already has its ctl") {
		t.Errorf("a second ctl stream: %v", err)
	}
	for range 2 {
		if _, err := sup.Open(ctx, KindExec, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sup.Open(ctx, KindExec, nil); err == nil || !strings.Contains(err.Error(), "the supervisor has 3 streams open") {
		t.Errorf("a fourth stream: %v", err)
	}
	g, err := guest.Accept(ctx)
	if err != nil || g.Kind() != KindCtl || string(g.header) != `{"kind":"ctl"}` {
		t.Fatalf("ctl %v %s, %v", g, g.header, err)
	}
}

// Open and Accept wait for the other end's hello, as long as the context
// lets them.
func TestWaitForHello(t *testing.T) {
	a, _ := socketPair(t)
	s := New(a, Supervisor, Options{})
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := s.Open(ctx, KindCtl, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("open: %v", err)
	}
	if _, err := s.Accept(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("accept: %v", err)
	}
}

func TestDeadlines(t *testing.T) {
	ctx := testCtx(t)
	sup, guest := pair(t, Options{Window: 16})
	st, err := sup.Open(ctx, KindExec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guest.Accept(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.SetDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("read: %v", err)
	}
	if n, err := st.Write([]byte("x")); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("write after the deadline: %d, %v", n, err)
	}
	// The 16 bytes of credit go first. Then 16 bytes are queued, and the
	// rest waits and times out.
	_ = st.SetWriteDeadline(time.Time{})
	if _, err := st.Write(make([]byte, 16)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { sup.mu.Lock(); defer sup.mu.Unlock(); return st.sendCred == 0 && st.out.len() == 0 })
	_ = st.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
	if n, err := st.Write(make([]byte, 100)); n != 16 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("write: %d, %v", n, err)
	}
	_ = st.SetReadDeadline(time.Time{})
	done := make(chan error, 1)
	go func() {
		_, err := st.Read(make([]byte, 1))
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	_ = st.SetReadDeadline(time.Now().Add(-time.Second))
	if err := <-done; !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("read after the deadline moved into the past: %v", err)
	}
	_ = st.SetReadDeadline(time.Time{})
	if n, err := st.Read(nil); n != 0 || err != nil {
		t.Errorf("an empty read: %d, %v", n, err)
	}
}

// Close of the session ends every stream on both ends.
func TestSessionClose(t *testing.T) {
	ctx := testCtx(t)
	sup, guest := pair(t, Options{})
	st, err := sup.Open(ctx, KindHolder, nil)
	if err != nil {
		t.Fatal(err)
	}
	g, err := guest.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	blocked := make(chan error, 1)
	go func() {
		_, err := g.Read(make([]byte, 1))
		blocked <- err
	}()
	if err := sup.Close(); err != nil {
		t.Fatal(err)
	}
	<-sup.Done()
	if !errors.Is(sup.Err(), net.ErrClosed) {
		t.Errorf("supervisor's error: %v", sup.Err())
	}
	if _, err := st.Write([]byte("x")); !errors.Is(err, net.ErrClosed) {
		t.Errorf("write: %v", err)
	}
	if _, err := sup.Open(ctx, KindHolder, nil); !errors.Is(err, net.ErrClosed) {
		t.Errorf("open: %v", err)
	}
	if _, err := sup.Accept(ctx); !errors.Is(err, net.ErrClosed) {
		t.Errorf("accept: %v", err)
	}
	if err := <-blocked; !errors.Is(err, ErrLinkClosed) {
		t.Errorf("guest's read: %v", err)
	}
	<-guest.Done()
	if !errors.Is(guest.Err(), ErrLinkClosed) {
		t.Errorf("guest's error: %v", guest.Err())
	}
	if err := st.Close(); err != nil {
		t.Errorf("close a stream of a closed link: %v", err)
	}
}

// Bytes that arrived before the other end closed its writes are still read
// after the link ends.
func TestReadAfterLinkEnds(t *testing.T) {
	ctx := testCtx(t)
	sup, guest := pair(t, Options{})
	st, err := sup.Open(ctx, KindHolder, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Write([]byte("last words")); err != nil {
		t.Fatal(err)
	}
	_ = st.CloseWrite()
	g, err := guest.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { g.s.mu.Lock(); defer g.s.mu.Unlock(); return g.remoteEOF })
	_ = sup.Close()
	<-guest.Done()
	got, err := io.ReadAll(g)
	if err != nil || string(got) != "last words" {
		t.Fatalf("read %q, %v", got, err)
	}
}
