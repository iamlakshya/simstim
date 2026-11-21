package client

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
)

const imageFetchTestRefV1 = "registry.example/team/dev:1"

func newImageFetchFixtureV1(t *testing.T, tier Tier, handle func(proto.Request) (any, *proto.Error)) (context.Context, *GrantRequest, *Conn, *runtimeDeliveryPeer) {
	t.Helper()
	isolateRuntimeDelivery(t)
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	imageManifestSource(t)
	c, peer := newRuntimeDeliveryPeer(t, "box", func(request proto.Request) (any, *proto.Error) {
		if request.Op == "image.status" {
			return missingImageStatus(t, request)
		}
		if handle != nil {
			return handle(request)
		}
		if request.Op == "image.pull" {
			var args image.Request
			if err := json.Unmarshal(request.Args, &args); err != nil {
				t.Error(err)
			}
			return image.Record{Ref: args.Ref, Digest: args.Digest, PlatformDigest: args.PlatformDigest, Arch: args.Arch, RootHash: strings.Repeat("a", 64), DataBlocks: 1, Size: 12288}, nil
		}
		if request.Op == "image.install" {
			return nil, nil
		}
		return nil, unexpectedRuntimeRequest(request)
	})
	c.Features = []string{"images"}
	bindImageFixturePeer(t, c)
	if tier != TierOwn {
		if err := SetTierContext(t.Context(), c.Host.Name, tier, c.HostID(), ""); err != nil {
			t.Fatal(err)
		}
	}
	r := NewGrantRequest(false, false)
	ctx, err := captureNewImagePolicy(WithGrantRequest(t.Context(), r), c, NewArgs{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			// An intentionally unacknowledged fixture retains the operation in the
			// assertion. After all fixture replies end, release its local files only.
			if r.preparationSettings != nil {
				_ = r.preparationSettings.Close()
			}
		}
	})
	return ctx, r, c, peer
}

func approveImageFetchV1(t *testing.T, ctx context.Context, c *Conn, mode string) context.Context {
	t.Helper()
	_, err := EnsureImage(ctx, c, imageFetchTestRefV1, ImageOptions{Mode: mode, Arch: "amd64"})
	var question *scopedGrantConfirmation
	if !errors.As(err, &question) || question.scope.kind != OutboundImageFetchV1 {
		t.Fatalf("missing original image question: %v", err)
	}
	approved, err := ConfirmGrant(ctx, err)
	if err != nil {
		t.Fatal(err)
	}
	return approved
}

func TestNewImageFetchRequiresItsOwnScopedAnswerOnEveryTier(t *testing.T) {
	for _, tier := range []Tier{TierOwn, TierThrowaway, TierShared} {
		t.Run(tier.String(), func(t *testing.T) {
			ctx, r, c, peer := newImageFetchFixtureV1(t, tier, nil)
			_, err := EnsureImage(ctx, c, imageFetchTestRefV1, ImageOptions{Mode: "host", Arch: "amd64"})
			var question *scopedGrantConfirmation
			var user *UserError
			if !errors.As(err, &question) || !errors.As(err, &user) || user.Kind != KindRefused || question.request != r || question.scope.tier != tier || !strings.Contains(question.Prompt, "Get it now?") {
				t.Fatalf("missing terminal refusal on %s: %v", tier, err)
			}
			if peer.count("image.pull", "") != 0 || peer.count("image.install", "") != 0 {
				t.Fatal("acquisition happened before the answer")
			}
			request := r.imageFetch.resolved.request.Clone()
			if err := c.Call(ctx, "image.pull", request, nil); err == nil {
				t.Fatal("raw RPC bypassed the unanswered intent")
			}
			if peer.count("image.pull", "") != 0 || peer.count("image.install", "") != 0 {
				t.Fatal("unapproved raw call reached the physical writer")
			}
			other := NewGrantRequest(false, false)
			if _, err := ConfirmGrant(WithGrantRequest(ctx, other), question); err == nil {
				t.Fatal("another request borrowed this answer")
			}
			approved, err := ConfirmGrant(ctx, question)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := EnsureImage(approved, c, imageFetchTestRefV1, ImageOptions{Mode: "host", Arch: "amd64"}); err != nil {
				t.Fatal(err)
			}
			if peer.count("image.pull", "") != 1 {
				t.Fatal("approved image was not acquired exactly once")
			}
		})
	}
}

func TestNewImageFetchRejectsChangedSelectionAndOriginalSources(t *testing.T) {
	for _, change := range []string{"image", "config", "host", "connection"} {
		t.Run(change, func(t *testing.T) {
			ctx, _, c, peer := newImageFetchFixtureV1(t, TierOwn, nil)
			ctx = approveImageFetchV1(t, ctx, c, "host")
			selected, connection := imageFetchTestRefV1, c
			switch change {
			case "image":
				selected = "registry.example/team/other:1"
			case "config":
				if err := os.MkdirAll(filepath.Dir(settingsPath()), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(settingsPath(), []byte("# replaced original descriptor\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "host":
				if err := SaveHosts(t.Context(), nil); err != nil {
					t.Fatal(err)
				}
				if err := SaveHosts(t.Context(), []Host{c.Host}); err != nil {
					t.Fatal(err)
				}
			case "connection":
				connection, _ = newRuntimeDeliveryPeer(t, c.Host.Name, func(request proto.Request) (any, *proto.Error) { return nil, unexpectedRuntimeRequest(request) })
				connection.Features = []string{"images"}
				bindImageFixturePeer(t, connection)
			}
			if _, err := EnsureImage(ctx, connection, selected, ImageOptions{Mode: "host", Arch: "amd64"}); err == nil {
				t.Fatal("changed selection or source reused the answer")
			}
			if peer.count("image.pull", "") != 0 || peer.count("image.install", "") != 0 {
				t.Fatal("changed source acquired bytes")
			}
		})
	}
}

func TestNewImageFetchCancellationAbortsOriginalBeginBeforeItsReply(t *testing.T) {
	began, beginRelease, beginEnded := make(chan string, 1), make(chan struct{}), make(chan struct{})
	var release sync.Once
	letBeginEnd := func() { release.Do(func() { close(beginRelease) }) }
	defer letBeginEnd()
	aborted := make(chan string, 1)
	ctx, r, c, peer := newImageFetchFixtureV1(t, TierOwn, func(request proto.Request) (any, *proto.Error) {
		var install image.Install
		if request.Op != "image.install" || json.Unmarshal(request.Args, &install) != nil {
			return nil, unexpectedRuntimeRequest(request)
		}
		switch install.Action {
		case "begin":
			began <- install.Request.Transfer
			<-beginRelease
			close(beginEnded)
			return image.Progress{Token: strings.Repeat("b", 32)}, nil
		case "abort":
			aborted <- install.Token
			letBeginEnd()
			<-beginEnded
			return nil, nil
		default:
			return nil, unexpectedRuntimeRequest(request)
		}
	})
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ctx = approveImageFetchV1(t, ctx, c, "laptop")
	done := make(chan error, 1)
	go func() {
		_, err := EnsureImage(ctx, c, imageFetchTestRefV1, ImageOptions{Mode: "laptop", Arch: "amd64"})
		done <- err
	}()
	var original string
	select {
	case original = <-began:
	case <-time.After(2 * time.Second):
		t.Fatal("original begin did not reach the writer")
	}
	if len(original) != 32 || original == strings.Repeat("b", 32) {
		t.Fatal("begin did not retain its separate original correlation")
	}
	cancel()
	select {
	case token := <-aborted:
		if token != original {
			t.Fatalf("abort used %q instead of original %q", token, original)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lost begin reply prevented original correlation abort")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation lost its original error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not join the original begin")
	}
	if peer.count("image.install", "chunk") != 0 || peer.count("image.install", "commit") != 0 {
		t.Fatal("cancelled begin copied or converted bytes")
	}
	if err := r.Close(); err != nil {
		t.Fatal("joined request could not close", err)
	}
	before := len(peer.snapshot())
	if err := r.Close(); err != nil || len(peer.snapshot()) != before {
		t.Fatal("close repeated the original transfer")
	}
}

func TestNewImageFetchUnknownBeginReplyRetainsOriginalOwnership(t *testing.T) {
	var original string
	ctx, r, c, peer := newImageFetchFixtureV1(t, TierOwn, func(request proto.Request) (any, *proto.Error) {
		var install image.Install
		if request.Op != "image.install" || json.Unmarshal(request.Args, &install) != nil {
			return nil, unexpectedRuntimeRequest(request)
		}
		if install.Action == "begin" {
			original = install.Request.Transfer
			return image.Progress{Token: "unknown"}, nil
		}
		if install.Action == "abort" {
			if install.Token != original {
				t.Error("unknown reply donated another cleanup token")
			}
			return nil, nil
		}
		return nil, unexpectedRuntimeRequest(request)
	})
	ctx = approveImageFetchV1(t, ctx, c, "laptop")
	if _, err := EnsureImage(ctx, c, imageFetchTestRefV1, ImageOptions{Mode: "laptop", Arch: "amd64"}); err == nil {
		t.Fatal("unknown begin reply accepted")
	}
	if original == "" || peer.count("image.install", "abort") != 1 {
		t.Fatal("unknown ACK lost its original cleanup correlation")
	}
	if err := r.Close(); err == nil || r.preparationClosed || r.imageFetch.closed {
		t.Fatal("unknown ACK released the original source and owner", err)
	}
	if peer.count("image.install", "chunk") != 0 || peer.count("image.install", "commit") != 0 {
		t.Fatal("unknown ACK permitted conversion")
	}
}

func TestNewImageFetchApprovedCopyUsesOriginalCorrelationAndPinnedPlan(t *testing.T) {
	var selected image.Request
	var received int64
	token := strings.Repeat("d", 32)
	ctx, r, c, peer := newImageFetchFixtureV1(t, TierOwn, func(request proto.Request) (any, *proto.Error) {
		var install image.Install
		if request.Op != "image.install" || json.Unmarshal(request.Args, &install) != nil {
			return nil, unexpectedRuntimeRequest(request)
		}
		switch install.Action {
		case "begin":
			selected = install.Request.Clone()
			if len(selected.Transfer) != 32 || selected.Transfer == token {
				t.Error("begin correlation was not independent")
			}
			selected.Transfer = ""
			return image.Progress{Token: token}, nil
		case "chunk":
			if install.Token != token {
				t.Error("chunk lost actual server token")
			}
			received += int64(len(install.Data))
			return image.Progress{Token: token, Received: received}, nil
		case "commit":
			if install.Token != token {
				t.Error("commit lost actual server token")
			}
			return image.Record{Ref: selected.Ref, Digest: selected.Digest, PlatformDigest: selected.PlatformDigest, Arch: selected.Arch, RootHash: strings.Repeat("a", 64), DataBlocks: 1, Size: 12288}, nil
		default:
			return nil, unexpectedRuntimeRequest(request)
		}
	})
	ctx = approveImageFetchV1(t, ctx, c, "laptop")
	if _, err := EnsureImage(ctx, c, imageFetchTestRefV1, ImageOptions{Mode: "laptop", Arch: "amd64"}); err != nil {
		t.Fatal(err)
	}
	if !r.imageFetch.complete || r.imageFetch.resolved.request.Transfer != "" || peer.count("image.install", "begin") != 1 || peer.count("image.install", "chunk") == 0 || peer.count("image.install", "commit") != 1 {
		t.Fatal("approved copy did not preserve its original pinned plan")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if peer.count("image.install", "abort") != 0 {
		t.Fatal("completed copy repeated cleanup")
	}
}

func TestNewImageFetchRefusesMissingNativeTransferFeatureBeforeConsent(t *testing.T) {
	ctx, r, c, peer := newImageFetchFixtureV1(t, TierOwn, nil)
	c.Features = []string{"images"}
	if _, err := EnsureImage(ctx, c, imageFetchTestRefV1, ImageOptions{Mode: "host", Arch: "amd64"}); err == nil {
		t.Fatal("old helper accepted an unjoinable image acquisition")
	}
	if r.imageFetch != nil || peer.count("image.pull", "") != 0 || peer.count("image.install", "") != 0 {
		t.Fatal("old helper received a correlated mutation or a misleading question")
	}
}

func TestNewImageFetchCloseCancelsAndJoinsOriginalHostPull(t *testing.T) {
	began, release, ended := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var correlation string
	ctx, r, c, _ := newImageFetchFixtureV1(t, TierOwn, func(request proto.Request) (any, *proto.Error) {
		if request.Op == "image.pull" {
			var selected image.Request
			if err := json.Unmarshal(request.Args, &selected); err != nil {
				t.Error(err)
			}
			correlation = selected.Transfer
			close(began)
			<-release
			close(ended)
			return nil, &proto.Error{Code: "image", Message: "original transfer cancelled", Hint: "Start a new image request."}
		}
		var install image.Install
		if request.Op == "image.install" && json.Unmarshal(request.Args, &install) == nil && install.Action == "abort" {
			if install.Token != correlation {
				t.Error("close lost the original host transfer")
			}
			unblock()
			<-ended
			return nil, nil
		}
		return nil, unexpectedRuntimeRequest(request)
	})
	ctx = approveImageFetchV1(t, ctx, c, "host")
	result := make(chan error, 1)
	go func() {
		_, err := EnsureImage(ctx, c, imageFetchTestRefV1, ImageOptions{Mode: "host", Arch: "amd64"})
		result <- err
	}()
	select {
	case <-began:
	case <-time.After(2 * time.Second):
		t.Fatal("host pull did not begin")
	}
	closed := make(chan error, 1)
	go func() { closed <- r.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal("original host pull did not join", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("close did not cancel and join original host pull")
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("close lost original cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("original caller survived close")
	}
	select {
	case <-ended:
	default:
		t.Fatal("source was released before original native handler ended")
	}
	if !r.imageFetch.closed || !r.preparationClosed {
		t.Fatal("joined request retained unfinished ownership")
	}
}

func TestNewImageFetchCancellationUsesServerTokenAfterBeginACK(t *testing.T) {
	chunkEntered, chunkRelease, chunkEnded := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var release sync.Once
	letChunkEnd := func() { release.Do(func() { close(chunkRelease) }) }
	defer letChunkEnd()
	aborted := make(chan string, 1)
	serverToken := strings.Repeat("e", 32)
	ctx, r, c, peer := newImageFetchFixtureV1(t, TierOwn, func(request proto.Request) (any, *proto.Error) {
		var install image.Install
		if request.Op != "image.install" || json.Unmarshal(request.Args, &install) != nil {
			return nil, unexpectedRuntimeRequest(request)
		}
		switch install.Action {
		case "begin":
			return image.Progress{Token: serverToken}, nil
		case "chunk":
			if install.Token != serverToken {
				t.Error("copy did not use acknowledged server token")
			}
			close(chunkEntered)
			<-chunkRelease
			close(chunkEnded)
			return nil, &proto.Error{Code: "image", Message: "original copy cancelled", Hint: "Start a new image request."}
		case "abort":
			aborted <- install.Token
			letChunkEnd()
			<-chunkEnded
			return nil, nil
		default:
			return nil, unexpectedRuntimeRequest(request)
		}
	})
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ctx = approveImageFetchV1(t, ctx, c, "laptop")
	done := make(chan error, 1)
	go func() {
		_, err := EnsureImage(ctx, c, imageFetchTestRefV1, ImageOptions{Mode: "laptop", Arch: "amd64"})
		done <- err
	}()
	select {
	case <-chunkEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("copy did not pass its original begin ACK")
	}
	cancel()
	select {
	case token := <-aborted:
		if token != serverToken {
			t.Fatal("post-ACK abort did not select actual server token", token)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not abort the acknowledged transfer")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("copy lost original cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("copy did not join original chunk")
	}
	if err := r.Close(); err != nil {
		t.Fatal("joined post-ACK copy retained unfinished ownership", err)
	}
	if r.imageFetch.resolved.request.Transfer != "" || peer.count("image.install", "abort") != 1 || peer.count("image.install", "commit") != 0 {
		t.Fatal("server token changed image identity or repeated cleanup")
	}
}

func TestNewImageFetchHostFailureNeedsOriginalAbortACK(t *testing.T) {
	abortEntered, abortRelease := make(chan string, 1), make(chan struct{})
	var release sync.Once
	unblock := func() { release.Do(func() { close(abortRelease) }) }
	defer unblock()
	ctx, r, c, peer := newImageFetchFixtureV1(t, TierOwn, func(request proto.Request) (any, *proto.Error) {
		if request.Op == "image.pull" {
			return nil, &proto.Error{Code: "image", Message: "registry unreachable", Hint: "Check this registry."}
		}
		var install image.Install
		if request.Op == "image.install" && json.Unmarshal(request.Args, &install) == nil && install.Action == "abort" {
			abortEntered <- install.Token
			<-abortRelease
			return nil, nil
		}
		return nil, unexpectedRuntimeRequest(request)
	})
	ctx = approveImageFetchV1(t, ctx, c, "host")
	done := make(chan error, 1)
	go func() {
		_, err := EnsureImage(ctx, c, imageFetchTestRefV1, ImageOptions{Mode: "host", Arch: "amd64"})
		done <- err
	}()
	select {
	case token := <-abortEntered:
		r.imageFetch.mu.Lock()
		original, closed := r.imageFetch.hostToken, r.imageFetch.closedTokens[r.imageFetch.hostToken]
		r.imageFetch.mu.Unlock()
		if token != original || closed {
			t.Fatal("arbitrary host failure became a cleanup receipt")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("failed host operation skipped original abort acknowledgement")
	}
	select {
	case err := <-done:
		t.Fatal("failure returned before original cleanup ACK", err)
	default:
	}
	unblock()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "registry unreachable") {
			t.Fatal("cleanup replaced original host failure", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup ACK did not join original result")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if peer.count("image.install", "abort") != 1 {
		t.Fatal("confirmed cleanup was repeated")
	}
}

func TestNewImageFetchFailedAbortRetainsOriginalSource(t *testing.T) {
	ctx, r, c, peer := newImageFetchFixtureV1(t, TierOwn, func(request proto.Request) (any, *proto.Error) {
		if request.Op == "image.pull" {
			return nil, &proto.Error{Code: "image", Message: "original image operation failed", Hint: "Check this registry."}
		}
		var install image.Install
		if request.Op == "image.install" && json.Unmarshal(request.Args, &install) == nil && install.Action == "abort" {
			return nil, &proto.Error{Code: "image", Message: "original cleanup is unconfirmed", Hint: "Keep the original connection open."}
		}
		return nil, unexpectedRuntimeRequest(request)
	})
	ctx = approveImageFetchV1(t, ctx, c, "host")
	if _, err := EnsureImage(ctx, c, imageFetchTestRefV1, ImageOptions{Mode: "host", Arch: "amd64"}); err == nil {
		t.Fatal("failed original operation and cleanup became success")
	}
	if peer.count("image.install", "abort") == 0 {
		t.Fatal("failed original operation skipped cleanup")
	}
	if err := r.Close(); err == nil || r.preparationClosed || r.imageFetch.closed {
		t.Fatal("failed abort released original source ownership", err)
	}
}

func TestNewImageFetchAbortTimeoutRejoinsSameOriginalReply(t *testing.T) {
	pullEntered, pullRelease, pullEnded := make(chan struct{}), make(chan struct{}), make(chan struct{})
	abortEntered, abortRelease := make(chan struct{}, 2), make(chan struct{})
	var release sync.Once
	unblock := func() { release.Do(func() { close(abortRelease); close(pullRelease) }) }
	defer unblock()
	ctx, r, c, peer := newImageFetchFixtureV1(t, TierOwn, func(request proto.Request) (any, *proto.Error) {
		if request.Op == "image.pull" {
			close(pullEntered)
			<-pullRelease
			close(pullEnded)
			return nil, &proto.Error{Code: "image", Message: "original host fetch cancelled", Hint: "Start a new image request."}
		}
		var install image.Install
		if request.Op == "image.install" && json.Unmarshal(request.Args, &install) == nil && install.Action == "abort" {
			abortEntered <- struct{}{}
			<-abortRelease
			<-pullEnded
			return nil, nil
		}
		return nil, unexpectedRuntimeRequest(request)
	})
	ctx = approveImageFetchV1(t, ctx, c, "host")
	result := make(chan error, 1)
	go func() {
		_, err := EnsureImage(ctx, c, imageFetchTestRefV1, ImageOptions{Mode: "host", Arch: "amd64"})
		result <- err
	}()
	select {
	case <-pullEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("original host pull did not begin")
	}
	intent := r.imageFetch
	intent.mu.Lock()
	correlation := intent.hostToken
	intent.mu.Unlock()
	first, cancel := context.WithTimeout(context.WithoutCancel(ctx), 25*time.Millisecond)
	err := intent.abort(first, correlation)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("local abort deadline did not remain a deadline", err)
	}
	select {
	case <-abortEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("original cleanup frame did not reach the host")
	}
	intent.mu.Lock()
	original := intent.aborts[correlation]
	intent.mu.Unlock()
	if original == nil {
		t.Fatal("local deadline discarded original pending abort")
	}
	second, cancel := context.WithTimeout(context.WithoutCancel(ctx), 25*time.Millisecond)
	err = intent.abort(second, correlation)
	cancel()
	if err == nil {
		t.Fatal("second local deadline invented a cleanup acknowledgement")
	}
	intent.mu.Lock()
	same := intent.aborts[correlation] == original
	intent.mu.Unlock()
	if !same || peer.count("image.install", "abort") != 1 {
		t.Fatal("retry replaced original pending cleanup instead of joining it")
	}
	unblock()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cleanup discarded original fetch cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("original cleanup reply did not join")
	}
	if err := r.Close(); err != nil {
		t.Fatal("original acknowledged cleanup could not close", err)
	}
	if peer.count("image.install", "abort") != 1 {
		t.Fatal("close repeated original acknowledged abort")
	}
}
