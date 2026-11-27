package client

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

type pullPeerFixture struct {
	mu               sync.Mutex
	supported        bool
	data             []byte
	begin            func(context.Context, api.PullBeginArgs, *api.PullExport) error
	chunk            func(api.PullReadArgs, *api.PullChunk)
	begins           []api.PullBeginArgs
	reads            []api.PullReadArgs
	acks             []api.PulledArgs
	abortContextLive bool
	ackError         error
}

func (p *pullPeerFixture) HasFeature(feature string) bool {
	return p.supported && feature == api.FeaturePullBundle
}
func transferFixture(value, out any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}
func (p *pullPeerFixture) Call(ctx context.Context, op string, args, out any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch op {
	case api.OpPullBegin:
		request := args.(api.PullBeginArgs)
		p.begins = append(p.begins, request)
		descriptor := api.PullExport{ID: request.ID, Primary: "primary2", Token: request.Token, Full: request.Full, Size: int64(len(p.data)), Empty: len(p.data) == 0, SeedHeads: []string{strings.Repeat("a", 40)}, DefaultRef: "refs/heads/fixture/primary2/topic"}
		if p.begin != nil {
			if err := p.begin(ctx, request, &descriptor); err != nil {
				return err
			}
		}
		return transferFixture(descriptor, out)
	case api.OpPullRead:
		request := args.(api.PullReadArgs)
		p.reads = append(p.reads, request)
		chunk := api.PullChunk{Data: append([]byte(nil), p.data[request.Offset:]...), Next: int64(len(p.data)), EOF: true}
		if p.chunk != nil {
			p.chunk(request, &chunk)
		}
		return transferFixture(chunk, out)
	case api.OpPulled:
		request := args.(api.PulledArgs)
		p.acks = append(p.acks, request)
		if request.Abort {
			p.abortContextLive = ctx.Err() == nil
		}
		if !request.Abort && p.ackError != nil {
			return p.ackError
		}
		return transferFixture(api.PulledResult{Commits: request.Commits}, out)
	}
	return errors.New("unexpected fixture operation")
}

func TestPullDownloadUsesExactBoundedRPCOffsets(t *testing.T) {
	peer := &pullPeerFixture{supported: true, data: []byte("bundle-fixture")}
	download, err := downloadPull(t.Context(), peer, "fixture2", false, "", 1024, "")
	if err != nil {
		t.Fatal(err)
	}
	directory := download.directory
	defer download.close()
	data, err := os.ReadFile(download.path)
	if err != nil || string(data) != "bundle-fixture" {
		t.Fatalf("%q %v", data, err)
	}
	if len(peer.begins) != 1 || len(peer.reads) != 1 || peer.reads[0].Offset != 0 || peer.reads[0].Token != peer.begins[0].Token {
		t.Fatal("RPC export identity or offsets changed")
	}
	download.close()
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("owned temporary bundle was retained", err)
	}
}

func TestPullDownloadRefusesMalformedChunksAndAborts(t *testing.T) {
	for _, kind := range []string{"empty", "backwards", "early-eof", "unfinished", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			peer := &pullPeerFixture{supported: true, data: []byte("fixture")}
			peer.chunk = func(_ api.PullReadArgs, chunk *api.PullChunk) {
				switch kind {
				case "empty":
					chunk.Data = nil
				case "backwards":
					chunk.Next = 0
				case "early-eof":
					chunk.Next = 1
					chunk.Data = []byte("f")
				case "unfinished":
					chunk.EOF = false
				case "oversized":
					chunk.Data = make([]byte, api.PullReadMax+1)
					chunk.Next = int64(len(chunk.Data))
				}
			}
			if download, err := downloadPull(t.Context(), peer, "fixture2", false, "", 1024, ""); err == nil {
				download.close()
				t.Fatal("malformed bundle chunk accepted")
			}
			if len(peer.acks) != 1 || !peer.acks[0].Abort || peer.acks[0].Token != peer.begins[0].Token {
				t.Fatal("malformed export did not release its exact owner")
			}
		})
	}
}

func TestPullBeginCancellationHasKnownDetachedAbortIdentity(t *testing.T) {
	peer := &pullPeerFixture{supported: true, data: []byte("fixture")}
	ctx, cancel := context.WithCancel(t.Context())
	peer.begin = func(_ context.Context, _ api.PullBeginArgs, _ *api.PullExport) error {
		cancel()
		return context.Canceled
	}
	if _, err := downloadPull(ctx, peer, "fixture2", false, "", 1024, ""); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(peer.acks) != 1 || !peer.acks[0].Abort || peer.acks[0].Token != peer.begins[0].Token || !peer.abortContextLive {
		t.Fatal("cancelled begin orphaned its export or used cancelled cleanup context")
	}
}

func TestEmptyPullReadsNothingAndInventsNoFetchedCommit(t *testing.T) {
	peer := &pullPeerFixture{supported: true}
	download, err := downloadPull(t.Context(), peer, "fixture2", false, "", 1024, "")
	if err != nil {
		t.Fatal(err)
	}
	defer download.close()
	if !download.export.Empty || download.path != "" || len(peer.reads) != 0 {
		t.Fatal("empty export allocated or read bundle data")
	}
	if err := finishPull(t.Context(), peer, download, nil, false); err != nil {
		t.Fatal(err)
	}
	if len(peer.acks) != 1 || len(peer.acks[0].Commits) != 0 {
		t.Fatal("empty export claimed fetched commits")
	}
}
