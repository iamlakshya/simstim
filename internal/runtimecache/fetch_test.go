package runtimecache

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
)

type memoryRoundTrip func(*http.Request) (*http.Response, error)

func (f memoryRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type countedBody struct {
	reader *bytes.Reader
	read   int
	closed bool
}

func (b *countedBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	b.read += n
	return n, err
}
func (b *countedBody) Close() error { b.closed = true; return nil }

func TestFetchStopsAtSignedSizeAndOneProbeByte(t *testing.T) {
	store, dir := testStore(t)
	item := testManifest([]byte("good"), "r1").Items[0]
	body := &countedBody{reader: bytes.NewReader(bytes.Repeat([]byte("x"), 1024))}
	client := &http.Client{Transport: memoryRoundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: body, ContentLength: -1, Header: make(http.Header), Request: r}, nil
	})}
	if err := store.Fetch(context.Background(), item, client, nil); err == nil {
		t.Fatal("oversize response accepted")
	}
	if body.read != int(item.Size)+1 {
		t.Fatalf("read %d bytes instead of size+one", body.read)
	}
	if !body.closed {
		t.Fatal("response body not closed")
	}
	assertNoParts(t, dir)
}

func TestFetchRefusesOversizeLengthBeforeReading(t *testing.T) {
	store, dir := testStore(t)
	item := testManifest([]byte("good"), "r1").Items[0]
	body := &countedBody{reader: bytes.NewReader([]byte("oversize"))}
	client := &http.Client{Transport: memoryRoundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: body, ContentLength: 8, Header: make(http.Header), Request: r}, nil
	})}
	if err := store.Fetch(context.Background(), item, client, nil); err == nil {
		t.Fatal("oversize content length accepted")
	}
	if body.read != 0 || !body.closed {
		t.Fatalf("oversize response was read or left open: %+v", body)
	}
	assertNoParts(t, dir)
}

func TestFetchVerifiesCachesAndKeepsCancellation(t *testing.T) {
	store, dir := testStore(t)
	data := []byte("data")
	item := testManifest(data, "r1").Items[0]
	calls := 0
	client := &http.Client{Transport: memoryRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data)), Header: make(http.Header), Request: r}, nil
	})}
	if err := store.Fetch(context.Background(), item, client, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Fetch(context.Background(), item, client, nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("cached runtime requested again: %d", calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Fetch(ctx, item, client, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	assertNoParts(t, dir)
}

func TestReleaseRedirectNeverDowngradesOrCarriesCredentials(t *testing.T) {
	client := HTTPClient()
	r, _ := http.NewRequest(http.MethodGet, "https://fixture.invalid/file", nil)
	r.Header.Set("Authorization", "fixture")
	r.Header.Set("Cookie", "fixture")
	if err := client.CheckRedirect(r, nil); err != nil {
		t.Fatal(err)
	}
	if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
		t.Fatal("redirect forwarded credentials")
	}
	r.URL.Scheme = "http"
	if err := client.CheckRedirect(r, nil); err == nil {
		t.Fatal("HTTP downgrade allowed")
	}
	r.URL.Scheme = "https"
	if err := client.CheckRedirect(r, make([]*http.Request, 5)); err == nil {
		t.Fatal("unbounded redirects")
	}
}
