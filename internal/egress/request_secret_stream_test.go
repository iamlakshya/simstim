package egress

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func requestStreamView(t *testing.T, values ...api.Secret) *requestSecretSnapshot {
	t.Helper()
	view, err := (&requestSecretSnapshot{}).withTripwireSentinels(values)
	if err != nil {
		t.Fatal(err)
	}
	return view
}

type requestStreamInput struct {
	pieces     []string
	offset     int
	oneByte    bool
	reads      atomic.Int32
	closes     atomic.Int32
	closeErr   error
	beforeRead func(int32)
}

func (r *requestStreamInput) Read(output []byte) (int, error) {
	count := r.reads.Add(1)
	if r.beforeRead != nil {
		r.beforeRead(count)
	}
	if r.oneByte && len(output) > 1 {
		output = output[:1]
	}
	for len(r.pieces) > 0 {
		if r.offset == len(r.pieces[0]) {
			r.pieces = r.pieces[1:]
			r.offset = 0
			continue
		}
		n := copy(output, r.pieces[0][r.offset:])
		r.offset += n
		return n, nil
	}
	return 0, io.EOF
}
func (r *requestStreamInput) Close() error { r.closes.Add(1); return r.closeErr }

func newRequestStreamTestReader(t *testing.T, input io.ReadCloser, get requestSecretStreamGetter) *requestSecretStreamReader {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	r, err := newRequestSecretStreamReader(ctx, input, get)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = r.Close() })
	return r
}

func TestRequestSecretStreamEveryInputSplitRefusesBeforeStandInBytes(t *testing.T) {
	const sentinel = "sentinel_ABCDEF"
	input := "before/" + sentinel + "/after"
	view := requestStreamView(t, sentinel)
	for split := 0; split <= len(input); split++ {
		source := &requestStreamInput{pieces: []string{input[:split], input[split:]}}
		reader := newRequestStreamTestReader(t, source, func(context.Context) (*requestSecretSnapshot, error) { return view, nil })
		got, err := io.ReadAll(reader)
		if !errors.Is(err, errRequestSecretStreamMisuse) || string(got) != "before/" {
			t.Fatalf("split%d released candidate bytes", split)
		}
		if source.closes.Load() != 1 {
			t.Fatal("refusal did not close the owned message exactly once")
		}
	}
}

func TestRequestSecretStreamOneByteInputAndOutputPreserveNearMisses(t *testing.T) {
	view := requestStreamView(t, "abcdefgh", "abcxyz", "bcdefgh")
	for _, text := range []string{"plain", "abcdefX", "abcxyX", "abc", "ab", "a", "", "xabcdefghX"} {
		source := &requestStreamInput{pieces: []string{text}, oneByte: true}
		reader := newRequestStreamTestReader(t, source, func(context.Context) (*requestSecretSnapshot, error) { return view, nil })
		var got bytes.Buffer
		var one [1]byte
		var terminal error
		for {
			n, err := reader.Read(one[:])
			got.Write(one[:n])
			if err != nil {
				terminal = err
				break
			}
		}
		if strings.Contains(text, "abcdefgh") {
			if !errors.Is(terminal, errRequestSecretStreamMisuse) || got.String() != "x" {
				t.Fatal("one-byte exact match escaped")
			}
		} else if terminal != io.EOF || got.String() != text {
			t.Fatal("proper prefix or near miss was changed")
		}
	}
}

func TestRequestSecretStreamColdCurrentAndMintedUnionHasNoEligibilityExemption(t *testing.T) {
	view := requestStreamView(t, "cold_ABC", "current_XYZ", "minted_123")
	if len(view.entries) != 0 {
		t.Fatal("cold fixture invented an eligible name")
	}
	for _, value := range view.tripwires {
		reader := newRequestStreamTestReader(t, io.NopCloser(strings.NewReader(string(value))), func(context.Context) (*requestSecretSnapshot, error) { return view, nil })
		got, err := io.ReadAll(reader)
		if len(got) != 0 || !errors.Is(err, errRequestSecretStreamMisuse) {
			t.Fatal("cold/public-only stand-in was ignored")
		}
	}
}

func TestRequestSecretStreamRawBytesDoNotClaimEncodedBodyDecoding(t *testing.T) {
	view := requestStreamView(t, "sentinel")
	for _, text := range []string{"%73entinel", "c2VudGluZWw=", `\u0073entinel`, `\x73entinel`} {
		reader := newRequestStreamTestReader(t, io.NopCloser(strings.NewReader(text)), func(context.Context) (*requestSecretSnapshot, error) { return view, nil })
		got, err := io.ReadAll(reader)
		if err != nil || string(got) != text {
			t.Fatal("raw payload reader silently added another encoding grammar")
		}
	}
}

func TestRequestSecretStreamRefreshAfterInputSeesNewGeneration(t *testing.T) {
	old := requestStreamView(t, "old_public")
	fresh := requestStreamView(t, "new_public")
	var current atomic.Pointer[requestSecretSnapshot]
	current.Store(old)
	source := &requestStreamInput{pieces: []string{"new_public"}, beforeRead: func(count int32) {
		if count == 1 {
			current.Store(fresh)
		}
	}}
	reader := newRequestStreamTestReader(t, source, func(context.Context) (*requestSecretSnapshot, error) { return current.Load(), nil })
	got, err := io.ReadAll(reader)
	if len(got) != 0 || !errors.Is(err, errRequestSecretStreamMisuse) {
		t.Fatal("publication during an input Read bypassed refresh")
	}
}

func TestRequestSecretStreamRefreshRechecksHeldPrefixAndKeepsMechanicalBound(t *testing.T) {
	old := requestStreamView(t, "public_prefix_long")
	fresh := requestStreamView(t, "public_X")
	var current atomic.Pointer[requestSecretSnapshot]
	current.Store(old)
	source := &requestStreamInput{pieces: []string{"public_", "X"}, beforeRead: func(count int32) {
		if count == 2 {
			current.Store(fresh)
		}
	}}
	reader := newRequestStreamTestReader(t, source, func(context.Context) (*requestSecretSnapshot, error) { return current.Load(), nil })
	got, err := io.ReadAll(reader)
	if len(got) != 0 || !errors.Is(err, errRequestSecretStreamMisuse) {
		t.Fatal("new generation did not rescan a held original prefix")
	}
	if reader.state.maxLength != len("public_prefix_long") {
		t.Fatal("shrinking generation invalidated the greatest-observed carry bound")
	}
}

func TestRequestSecretStreamRefreshBeforeNextOutputRechecksBufferedBytes(t *testing.T) {
	old := requestStreamView(t, "unrelated")
	fresh := requestStreamView(t, "bc")
	var current atomic.Pointer[requestSecretSnapshot]
	current.Store(old)
	reader := newRequestStreamTestReader(t, io.NopCloser(strings.NewReader("abc")), func(context.Context) (*requestSecretSnapshot, error) { return current.Load(), nil })
	var one [1]byte
	if n, err := reader.Read(one[:]); err != nil || n != 1 || one[0] != 'a' {
		t.Fatal("initial unrelated byte did not pass")
	}
	current.Store(fresh)
	got, err := io.ReadAll(reader)
	if len(got) != 0 || !errors.Is(err, errRequestSecretStreamMisuse) {
		t.Fatal("new output Read used a stale buffered-byte view")
	}
}

type requestStreamObservedInput struct {
	io.ReadCloser
	calls  atomic.Int32
	second chan struct{}
}

func (r *requestStreamObservedInput) Read(output []byte) (int, error) {
	if r.calls.Add(1) == 2 {
		close(r.second)
	}
	return r.ReadCloser.Read(output)
}

func TestRequestSecretStreamWithholdsPrefixUntilMessageContinuation(t *testing.T) {
	view := requestStreamView(t, "sentinel")
	pipe, writer := io.Pipe()
	defer writer.Close()
	source := &requestStreamObservedInput{ReadCloser: pipe, second: make(chan struct{})}
	reader := newRequestStreamTestReader(t, source, func(context.Context) (*requestSecretSnapshot, error) { return view, nil })
	result := make(chan error, 1)
	go func() {
		var out [32]byte
		n, err := reader.Read(out[:])
		if n != 0 {
			result <- errors.New("prefix escaped before continuation")
			return
		}
		result <- err
	}()
	if _, err := writer.Write([]byte("sent")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-source.second:
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not hold a viable prefix")
	}
	select {
	case <-result:
		t.Fatal("transport Read end was treated as EOF")
	default:
	}
	if _, err := writer.Write([]byte("inel")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, errRequestSecretStreamMisuse) {
			t.Fatal("continued stand-in did not refuse")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("completed candidate did not terminate")
	}
}

func TestRequestSecretStreamCancellationUnblocksReadAndOwnedClose(t *testing.T) {
	view := requestStreamView(t, "sentinel")
	pipe, writer := io.Pipe()
	defer writer.Close()
	source := &requestStreamObservedInput{ReadCloser: pipe, second: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reader, err := newRequestSecretStreamReader(ctx, source, func(context.Context) (*requestSecretSnapshot, error) { return view, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	result := make(chan error, 1)
	go func() { _, err := io.ReadAll(reader); result <- err }()
	if _, err := writer.Write([]byte("sent")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-source.second:
	case <-time.After(5 * time.Second):
		t.Fatal("input Read was not active")
	}
	cancel()
	closed := make(chan struct{})
	go func() { _ = reader.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not cancel/join an active owned input Read")
	}
	select {
	case err := <-result:
		if err == nil || err == io.EOF {
			t.Fatal("cancelled prefix became a clean EOF")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("synchronous reader remained blocked")
	}
}

func TestRequestSecretStreamLongNearMissReleasesWithoutEOF(t *testing.T) {
	prefix := "prefix_" + strings.Repeat("x", 4096)
	view := requestStreamView(t, api.Secret(prefix+"Z"))
	pipe, writer := io.Pipe()
	defer writer.Close()
	reader := newRequestStreamTestReader(t, pipe, func(context.Context) (*requestSecretSnapshot, error) { return view, nil })
	written := make(chan error, 1)
	go func() { _, err := writer.Write([]byte(prefix + "Y")); written <- err }()
	got := make([]byte, len(prefix)+1)
	if _, err := io.ReadFull(reader, got); err != nil || string(got) != prefix+"Y" {
		t.Fatal("disqualified long prefix waited for EOF or was changed")
	}
	if cap(reader.state.buffer) > reader.state.maxLength+requestSecretStreamChunk {
		t.Fatal("carry allocation exceeded its mechanical bound")
	}
	select {
	case err := <-written:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("near-miss source remained blocked")
	}
}

func TestRequestSecretStreamBackpressureDoesNotPrefetchWholeMessage(t *testing.T) {
	view := requestStreamView(t, "secret")
	source := &requestStreamInput{pieces: []string{strings.Repeat("x", 4*requestSecretStreamChunk)}}
	reader := newRequestStreamTestReader(t, source, func(context.Context) (*requestSecretSnapshot, error) { return view, nil })
	var one [1]byte
	if n, err := reader.Read(one[:]); err != nil || n != 1 || one[0] != 'x' {
		t.Fatal("ordinary byte did not pass")
	}
	if source.reads.Load() != 1 || len(reader.state.pending()) != requestSecretStreamChunk-1 {
		t.Fatal("reader prefetched beyond caller demand")
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if source.reads.Load() != 1 || source.closes.Load() != 1 {
		t.Fatal("Close leaked a read worker or repeated input Close")
	}
}

func TestRequestSecretStreamMissingOrInvalidViewFailsBeforeInput(t *testing.T) {
	for _, view := range []*requestSecretSnapshot{nil, {tripwires: []api.Secret{""}}, {tripwires: []api.Secret{"z", "a"}}, {tripwires: []api.Secret{"a", "a"}}, {tripwires: []api.Secret{"a\x00b"}}} {
		source := &requestStreamInput{pieces: []string{"private-input"}}
		reader := newRequestStreamTestReader(t, source, func(context.Context) (*requestSecretSnapshot, error) { return view, nil })
		got, err := io.ReadAll(reader)
		if len(got) != 0 || !errors.Is(err, errRequestSecretStreamView) || source.reads.Load() != 0 {
			t.Fatal("missing/invalid canonical view fell back to passthrough")
		}
	}
}

func TestRequestSecretStreamGetterAndInputErrorsStayContentFree(t *testing.T) {
	const private = "private-getter-credential"
	view := requestStreamView(t, "sentinel")
	for _, afterRead := range []bool{false, true} {
		source := &requestStreamInput{pieces: []string{private}}
		reader := newRequestStreamTestReader(t, source, func(context.Context) (*requestSecretSnapshot, error) {
			if afterRead && source.reads.Load() == 0 {
				return view, nil
			}
			return nil, errors.New(private)
		})
		got, err := io.ReadAll(reader)
		if len(got) != 0 || err == nil || strings.Contains(err.Error(), private) {
			t.Fatal("getter failure leaked source or details")
		}
	}
	source := &requestStreamInput{closeErr: errors.New(private)}
	reader := newRequestStreamTestReader(t, source, func(context.Context) (*requestSecretSnapshot, error) { return view, nil })
	_, err := io.ReadAll(reader)
	if !errors.Is(err, errRequestSecretStreamRead) || strings.Contains(err.Error(), private) {
		t.Fatal("input Close failure became clean EOF or leaked details")
	}
	if err := reader.Close(); !errors.Is(err, errRequestSecretStreamRead) || source.closes.Load() != 1 {
		t.Fatal("repeated Close changed owned Close count")
	}
}

func TestRequestSecretStreamFacadeFormattingConcurrentWithReadAndClose(t *testing.T) {
	view := requestStreamView(t, "sentinel")
	pipe, writer := io.Pipe()
	defer writer.Close()
	reader := newRequestStreamTestReader(t, pipe, func(context.Context) (*requestSecretSnapshot, error) { return view, nil })
	result := make(chan struct{})
	go func() { _, _ = io.ReadAll(reader); close(result) }()
	for i := 0; i < 32; i++ {
		for _, value := range []any{reader, *reader} {
			for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X"} {
				if strings.Contains(fmt.Sprintf(verb, value), "sentinel") {
					t.Fatal("formatter traversed the canonical view")
				}
			}
		}
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("formatting/Close left the reader alive")
	}
}

func TestRequestSecretStreamEmptyCanonicalUnionAndConstructorOwnership(t *testing.T) {
	view := requestStreamView(t)
	reader := newRequestStreamTestReader(t, io.NopCloser(strings.NewReader("ordinary payload")), func(context.Context) (*requestSecretSnapshot, error) { return view, nil })
	got, err := io.ReadAll(reader)
	if err != nil || string(got) != "ordinary payload" {
		t.Fatal("empty valid union changed unrelated payload")
	}
	source := &requestStreamInput{}
	if _, err := newRequestSecretStreamReader(context.Background(), source, nil); err == nil || source.closes.Load() != 0 {
		t.Fatal("failed constructor took input ownership")
	}
}

func TestRequestSecretStreamWebSocketFragmentTripwireKeepsControlProgress(t *testing.T) {
	view := requestStreamView(t, "public_sentinel")
	client := func(ctx context.Context, _ byte, input io.ReadCloser) (io.ReadCloser, error) {
		return newRequestSecretStreamReader(ctx, input, func(context.Context) (*requestSecretSnapshot, error) { return view, nil })
	}
	wrapper, peer, _ := wsTestConnection(t, webSocketFilters{Server: wsPassFilter, Client: client})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	written := make(chan error, 1)
	go func() {
		wire := append(wsTestWire(1, false, true, []byte("public_")), wsTestWire(webSocketPing, true, true, []byte("alive"))...)
		if _, err := wrapper.Write(wire); err != nil {
			written <- err
			return
		}
		<-release
		_, err := wrapper.Write(wsTestWire(0, true, true, []byte("sentinel")))
		written <- err
	}()
	control, err := wsReadTestFrame(peer)
	if err != nil || control.opcode != webSocketPing || !control.masked || string(control.payload) != "alive" {
		t.Fatal("client tripwire prefix blocked a valid control")
	}
	once.Do(func() { close(release) })
	closeFrame, err := wsReadTestFrame(wrapper)
	if err != nil || closeFrame.opcode != webSocketClose || len(closeFrame.payload) < 2 || closeFrame.payload[0] != 3 || closeFrame.payload[1] != 240 {
		t.Fatal("client stand-in did not produce safe1008 before origin payload")
	}
	var probe [1]byte
	if _, err := peer.Read(probe[:]); err == nil {
		t.Fatal("candidate stand-in bytes reached origin")
	}
	select {
	case <-written:
	case <-time.After(5 * time.Second):
		t.Fatal("refused upload retained a writer")
	}
}

type requestStreamTerminalInput struct {
	payload string
	err     error
	sent    bool
}

func (r *requestStreamTerminalInput) Read(output []byte) (int, error) {
	if r.sent {
		return 0, io.EOF
	}
	r.sent = true
	return copy(output, r.payload), r.err
}
func (*requestStreamTerminalInput) Close() error { return nil }

func TestRequestSecretStreamSourceErrorIsNotTrueMessageEOF(t *testing.T) {
	view := requestStreamView(t, "sentinel")
	for _, tc := range []struct {
		payload   string
		sourceErr error
		want      string
		misuse    bool
	}{
		{"sentinel", io.EOF, "", true},
		{"sent", io.EOF, "sent", false},
		{"sent", errors.New("private-source-detail"), "", false},
		{"ordinary", errors.New("private-source-detail"), "", false},
	} {
		reader := newRequestStreamTestReader(t, &requestStreamTerminalInput{payload: tc.payload, err: tc.sourceErr}, func(context.Context) (*requestSecretSnapshot, error) { return view, nil })
		got, err := io.ReadAll(reader)
		if string(got) != tc.want || strings.Contains(fmt.Sprint(err), "private-source-detail") {
			t.Fatal("source error released uncertain bytes or details")
		}
		if tc.misuse && !errors.Is(err, errRequestSecretStreamMisuse) {
			t.Fatal("bytes with true EOF hid a complete candidate")
		}
		if tc.sourceErr != io.EOF && !errors.Is(err, errRequestSecretStreamRead) {
			t.Fatal("source failure became clean EOF")
		}
		if tc.sourceErr == io.EOF && !tc.misuse && err != nil {
			t.Fatal("proper EOF prefix did not pass")
		}
	}
}

func TestRequestSecretStreamCompleteShortMatchDoesNotWaitForLongerVariant(t *testing.T) {
	view := requestStreamView(t, "ab", "abcdef")
	pipe, writer := io.Pipe()
	defer writer.Close()
	reader := newRequestStreamTestReader(t, pipe, func(context.Context) (*requestSecretSnapshot, error) { return view, nil })
	result := make(chan error, 1)
	go func() {
		got, err := io.ReadAll(reader)
		if len(got) != 0 {
			result <- errors.New("complete prefix candidate escaped")
			return
		}
		result <- err
	}()
	if _, err := writer.Write([]byte("ab")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, errRequestSecretStreamMisuse) {
			t.Fatal("complete shorter candidate did not fail")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shorter full match waited for an unnecessary longer continuation")
	}
}

func TestRequestSecretStreamOrdinaryShortPayloadReturnsBeforeEOF(t *testing.T) {
	view := requestStreamView(t, api.Secret("very_long_"+strings.Repeat("x", 4096)))
	pipe, writer := io.Pipe()
	defer writer.Close()
	reader := newRequestStreamTestReader(t, pipe, func(context.Context) (*requestSecretSnapshot, error) { return view, nil })
	result := make(chan string, 1)
	go func() {
		var buffer [64]byte
		n, err := reader.Read(buffer[:])
		if err != nil {
			result <- "error"
			return
		}
		result <- string(buffer[:n])
	}()
	if _, err := writer.Write([]byte("ready\n\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		if got != "ready\n\n" {
			t.Fatal("short accepted payload changed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ordinary payload stalled behind an unrelated maximum-length stand-in")
	}
}

func TestRequestSecretStreamCancellationLeavesCanonicalGetterGate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	source := &requestStreamInput{}
	started := make(chan struct{})
	reader, err := newRequestSecretStreamReader(ctx, source, func(ctx context.Context) (*requestSecretSnapshot, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	result := make(chan error, 1)
	go func() { _, err := io.ReadAll(reader); result <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("getter did not enter its cancellable gate")
	}
	cancel()
	closed := make(chan struct{})
	go func() { _ = reader.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close joined a nonresponsive canonical gate")
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled getter succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("getter remained blocked")
	}
	if source.reads.Load() != 0 || source.closes.Load() != 1 {
		t.Fatal("failed getter read input or lost ownership cleanup")
	}
}
