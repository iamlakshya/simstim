package inputtransfer

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

type bufferStream struct {
	io.Reader
	output bytes.Buffer
}

func (s *bufferStream) Write(data []byte) (int, error) { return s.output.Write(data) }
func (*bufferStream) CloseWrite() error                { return nil }
func (*bufferStream) Close() error                     { return nil }

func declarationWire(t *testing.T, plan api.SessionInputs, scope Scope) []byte {
	t.Helper()
	summary, err := plan.Summary(api.DefaultCopyMax, api.DefaultSeedMax)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err = WriteSummary(t.Context(), &out, summary); err != nil {
		t.Fatal(err)
	}
	writer := proto.NewWriter(&out)
	for index, file := range plan.Canonical().Files {
		if err = sendEvent(t.Context(), writer, scope, "input.file", index, &file); err != nil {
			t.Fatal(err)
		}
	}
	if err = sendEvent(t.Context(), writer, scope, "input.declarations_end", len(plan.Files), nil); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestWholeDeclarationValidationPrecedesAnyGuestWriteOrOpen(t *testing.T) {
	plan, _ := transferPlan(t, nil)
	plan.Files = []api.InputFile{fileDeclaration("/home/agent/first", nil), fileDeclaration("/home/agent/kept/private", nil)}
	summary, err := plan.Summary(api.DefaultCopyMax, api.DefaultSeedMax)
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{Session: "session2", Nonce: plan.Nonce, Digest: summary.Digest}
	wire := declarationWire(t, plan, scope)
	options, directory := receiverFixture(t)
	options.Scope, options.Protected = scope, []string{"/home/agent/kept"}
	var output bytes.Buffer
	if _, err = Receive(t.Context(), bytes.NewReader(wire), &output, options); err == nil {
		t.Fatal("protected late declaration accepted")
	}
	if _, err = os.Stat(filepath.Join(directory, "home", "agent", "first")); !os.IsNotExist(err) {
		t.Fatal("valid prefix wrote before the complete declaration table validated")
	}
	spool, err := os.CreateTemp(t.TempDir(), "metadata-")
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	stream := &bufferStream{Reader: bytes.NewReader(wire)}
	opened := false
	err = Forward(t.Context(), stream, ForwardOptions{Scope: scope, Summary: summary, CopyMax: options.CopyMax, SeedMax: options.SeedMax,
		Protected: options.Protected, Spool: spool, OpenGuest: func(context.Context) (Stream, error) {
			opened = true
			return nil, nil
		}, Finish: func(context.Context) error { return nil }})
	if err == nil || opened {
		t.Fatalf("unverified declaration table opened the guest: %v", err)
	}
}

func TestCreationFailureReachesEveryImplicitAncestorAndRollsBack(t *testing.T) {
	options, directory := receiverFixture(t)
	guardCalls := 0
	options.BeforeCreate = func(ctx context.Context, _ *os.Root) error {
		guardCalls++
		if guardCalls == 2 {
			return &proto.Error{Code: "disk_full", Message: "fixture budget exhausted", Hint: "Free some space."}
		}
		return ctx.Err()
	}
	file := api.InputFile{Path: "/home/agent/new/one/two/empty", Mode: 0700, SHA256: api.EmptyInputHash, Directory: true}
	err := receiveFile(t.Context(), bufio.NewReader(bytes.NewReader(inputFileWire(t, file, nil))), options, 0, file)
	if err == nil || guardCalls != 2 {
		t.Fatalf("ancestor bypassed current budget/error guard: %d %v", guardCalls, err)
	}
	if _, err := os.Stat(filepath.Join(directory, "home", "agent", "new")); !os.IsNotExist(err) {
		t.Fatal("ordinary failure retained an owned empty ancestor")
	}
}
