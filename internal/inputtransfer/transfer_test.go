package inputtransfer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// This fixture exercises the real domain framing and half-close. It is not a
// native process, backend, UID, namespace or owned-runner proof.
type pipeStream struct {
	in   *io.PipeReader
	out  *io.PipeWriter
	once sync.Once
}

func (p *pipeStream) Read(data []byte) (int, error)  { return p.in.Read(data) }
func (p *pipeStream) Write(data []byte) (int, error) { return p.out.Write(data) }
func (p *pipeStream) CloseWrite() error              { return p.out.Close() }
func (p *pipeStream) Close() error {
	p.once.Do(func() { _ = p.in.Close(); _ = p.out.Close() })
	return nil
}

func streamPair(t *testing.T) (*pipeStream, *pipeStream) {
	t.Helper()
	firstRead, secondWrite := io.Pipe()
	secondRead, firstWrite := io.Pipe()
	first := &pipeStream{in: firstRead, out: firstWrite}
	second := &pipeStream{in: secondRead, out: secondWrite}
	t.Cleanup(func() { _ = first.Close(); _ = second.Close() })
	return first, second
}

func receiverFixture(t *testing.T) (ReceiveOptions, string) {
	t.Helper()
	directory := t.TempDir()
	if err := os.MkdirAll(filepath.Join(directory, "home", "agent"), 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close(); _ = stage.Close() })
	return ReceiveOptions{Root: root, Stage: stage, CopyMax: api.DefaultCopyMax, SeedMax: api.DefaultSeedMax,
		BeforeCreate: func(ctx context.Context, _ *os.Root) error { return ctx.Err() }}, directory
}

func transferPlan(t *testing.T, data []byte) (api.SessionInputs, Scope) {
	t.Helper()
	hash := sha256.Sum256(data)
	plan := api.SessionInputs{Version: api.SessionInputsV1, Nonce: api.InputNonce(strings.Repeat("a", 64)),
		Files: []api.InputFile{{Path: "/home/agent/input", Size: int64(len(data)), Mode: 0600, SHA256: hex.EncodeToString(hash[:])}}}
	summary, err := plan.Summary(api.DefaultCopyMax, api.DefaultSeedMax)
	if err != nil {
		t.Fatal(err)
	}
	return plan, Scope{Session: "session2", Nonce: plan.Nonce, Digest: summary.Digest}
}

func completeFixture(ctx context.Context, stream Stream, options ReceiveOptions) (err error) {
	defer func() {
		if err != nil {
			_ = ReportFailure(stream, err)
			_ = stream.Close()
		}
	}()
	result, err := Receive(ctx, stream, stream, options)
	if err != nil {
		return err
	}
	if err = Complete(ctx, stream, options.Scope, result.Files+result.Repositories); err != nil {
		return err
	}
	return stream.CloseWrite()
}

func TestInputDomainTransferKeepsRawBytesAndCompletesAfterEOF(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	data := bytes.Repeat([]byte{0, 0xff, 0x1b, '\r', '\n'}, 30000)
	plan, scope := transferPlan(t, data)
	options, directory := receiverFixture(t)
	options.Scope = scope
	client, guest := streamPair(t)
	done := make(chan error, 1)
	go func() { done <- completeFixture(ctx, guest, options) }()
	err := Send(ctx, client, scope, plan, Sources{File: func(_ context.Context, _ api.InputFile, output io.Writer) error {
		_, err := output.Write(data)
		return err
	}}, options.CopyMax, options.SeedMax)
	if err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	copied, err := os.ReadFile(filepath.Join(directory, "home", "agent", "input"))
	if err != nil || !bytes.Equal(copied, data) {
		t.Fatalf("raw copy changed: %v", err)
	}
}

func TestInputDomainForwardStoresMetadataAndAuditsExactFiles(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	data := []byte("declared data")
	plan, scope := transferPlan(t, data)
	summary, err := plan.Summary(api.DefaultCopyMax, api.DefaultSeedMax)
	if err != nil {
		t.Fatal(err)
	}
	options, _ := receiverFixture(t)
	options.Scope = scope
	client, fromClient := streamPair(t)
	toGuest, guest := streamPair(t)
	spool, err := os.CreateTemp(t.TempDir(), "metadata-")
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	forwardDone, guestDone := make(chan error, 1), make(chan error, 1)
	go func() { guestDone <- completeFixture(ctx, guest, options) }()
	var audited []string
	go func() {
		forwardDone <- Forward(ctx, fromClient, ForwardOptions{Scope: scope, Summary: summary,
			CopyMax: options.CopyMax, SeedMax: options.SeedMax, Spool: spool,
			OpenGuest: func(context.Context) (Stream, error) { return toGuest, nil },
			Audit:     func(file api.InputFile) error { audited = append(audited, file.Path); return nil },
			Finish:    func(context.Context) error { return spool.Close() }})
	}()
	if err = Send(ctx, client, scope, plan, Sources{File: func(_ context.Context, _ api.InputFile, out io.Writer) error {
		_, err := out.Write(data)
		return err
	}}, options.CopyMax, options.SeedMax); err != nil {
		t.Fatal(err)
	}
	if err = <-forwardDone; err != nil {
		t.Fatal(err)
	}
	if err = <-guestDone; err != nil {
		t.Fatal(err)
	}
	if len(audited) != 1 || audited[0] != plan.Files[0].Path {
		t.Fatalf("audit = %q", audited)
	}
	metadata, err := os.ReadFile(spool.Name())
	if err != nil || bytes.Contains(metadata, data) {
		t.Fatal("host declaration spool retained copy payload")
	}
}

func TestInputSummaryLargerThanCtlFrameUsesSameChunkFraming(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	data := []byte("x")
	hash := sha256.Sum256(data)
	plan := api.SessionInputs{Version: api.SessionInputsV1, Nonce: api.InputNonce(strings.Repeat("a", 64)), Identity: api.GitIdentity{Known: true},
		Repositories: []api.InputRepository{{Name: "source", SourceBranch: "refs/heads/laptop-seed", Commit: strings.Repeat("b", 40),
			Origin: strings.Repeat("x", 128<<10), Size: 1, SHA256: hex.EncodeToString(hash[:]), ObjectFormat: "sha1"}}}
	summary, err := plan.Summary(api.DefaultCopyMax, api.DefaultSeedMax)
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{Session: "session2", Nonce: plan.Nonce, Digest: summary.Digest}
	options, _ := receiverFixture(t)
	options.Scope = scope
	client, guest := streamPair(t)
	bundleClient, bundleGuest := streamPair(t)
	done := make(chan error, 1)
	go func() {
		result, err := Receive(ctx, io.MultiReader(guest, bundleGuest), guest, options)
		if err == nil {
			err = Complete(ctx, guest, scope, result.Files+result.Repositories)
		}
		_ = bundleGuest.CloseWrite()
		_ = guest.CloseWrite()
		done <- err
	}()
	opened := false
	if err = Send(ctx, client, scope, plan, Sources{OpenBundle: func(_ context.Context, header Header) (Stream, error) {
		if header.Purpose != "inputs" || header.Scope != scope {
			return nil, errors.New("bundle scope changed")
		}
		opened = true
		return bundleClient, nil
	}, Repository: func(_ context.Context, _ api.InputRepository, out io.Writer) error {
		_, err := out.Write(data)
		return err
	}}, options.CopyMax, options.SeedMax); err != nil {
		t.Fatal(err)
	}
	if !opened {
		t.Fatal("seed bytes did not use the literal bundle stream")
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if err = VerifyBundle(ctx, options.Stage, 0, plan.Repositories[0]); err != nil {
		t.Fatal(err)
	}
}

func TestInputSenderCancellationClosesOwnedStreamAndJoinsReceiver(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	plan, scope := transferPlan(t, []byte("expected"))
	options, _ := receiverFixture(t)
	options.Scope = scope
	client, guest := streamPair(t)
	done := make(chan error, 1)
	go func() { done <- completeFixture(ctx, guest, options) }()
	err := Send(ctx, client, scope, plan, Sources{File: func(ctx context.Context, _ api.InputFile, _ io.Writer) error {
		cancel()
		return ctx.Err()
	}}, options.CopyMax, options.SeedMax)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("partial input completed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("receiver outlived its owned stream")
	}
}
