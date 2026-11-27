package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/inputtransfer"
)

func inputFinishFixture(t *testing.T) (*Supervisor, *startupInput) {
	t.Helper()
	scope := inputtransfer.Scope{Session: "primary234", Nonce: api.InputNonce(strings.Repeat("a", 64)), Digest: strings.Repeat("b", 64)}
	summary := api.InputSummary{Version: 1, Nonce: scope.Nonce, Digest: scope.Digest}
	spool, err := os.CreateTemp(t.TempDir(), "declarations-")
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := newInputCompletion(spool)
	if err != nil {
		t.Fatal(err)
	}
	receipt.senderDone, receipt.guestDone = true, true
	input := &startupInput{scope: scope, summary: summary, runGeneration: "original-run", completion: receipt}
	s := &Supervisor{id: scope.Session, generation: input.runGeneration, lateInput: input, firstSaved: true, rec: record{phase: host.LifeRunning}, m: &host.Manifest{ID: scope.Session, Schema: 1, Inputs: host.CloneInputSummary(&summary), CopiesPending: true, Run: host.Runtime{Generation: input.runGeneration}}}
	t.Cleanup(func() { _ = receipt.cleanup() })
	return s, input
}
func finishBegin(input *startupInput) inputtransfer.BeginArgs {
	return inputtransfer.BeginArgs{Scope: input.scope, Summary: host.CloneInputSummary(&input.summary), FinishOnly: true, RunGeneration: input.runGeneration}
}
func TestLateInputFinishSaveFailureRecoversSameOperationAndLostAck(t *testing.T) {
	s, input := inputFinishFixture(t)
	old := saveManifest
	t.Cleanup(func() { saveManifest = old })
	saves := 0
	saveManifest = func(*host.Manifest) error { saves++; return syscall.EIO }
	if err := s.finishInput(context.Background(), input, true); !errors.Is(err, syscall.EIO) || !s.m.CopiesPending {
		t.Fatal("failed Finish published success", err)
	}
	if !input.completion.closed || !input.completion.removed {
		t.Fatal("failed durable Finish retained open metadata")
	}
	saveManifest = func(*host.Manifest) error { saves++; return nil }
	// This call has only the original exact receipt, as a new CLI process does.
	if err := s.recoverInputFinish(context.Background(), finishBegin(input)); err != nil || s.m.CopiesPending {
		t.Fatal("exact same-operation recovery failed", err)
	}
	if err := s.recoverInputFinish(context.Background(), finishBegin(input)); err != nil || s.m.CopiesPending {
		t.Fatal("lost final ACK could not recover terminal receipt", err)
	}
	if saves != 2 {
		t.Fatal("recovery repeated work outside the canonical writer")
	}
	if input.completion.spool != nil {
		if _, err := input.completion.spool.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
			t.Fatal("recovery reopened or read metadata")
		}
	}
}
func TestLateInputFinishCloseErrorAndIdentityReplacementRemainRetryable(t *testing.T) {
	for _, kind := range []string{"closed-descriptor", "replaced-path"} {
		t.Run(kind, func(t *testing.T) {
			s, input := inputFinishFixture(t)
			receipt := input.completion
			old := saveManifest
			t.Cleanup(func() { saveManifest = old })
			writes := 0
			saveManifest = func(*host.Manifest) error { writes++; return nil }
			var restore func()
			if kind == "closed-descriptor" {
				if err := receipt.spool.Close(); err != nil {
					t.Fatal(err)
				}
				restore = func() {}
			} else {
				backup := receipt.name + "-original"
				if err := os.Rename(receipt.name, backup); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(receipt.name, 0700); err != nil {
					t.Fatal(err)
				}
				foreign := filepath.Join(receipt.name, "foreign")
				if err := os.WriteFile(foreign, []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
				restore = func() {
					if _, err := os.Stat(foreign); err != nil {
						t.Fatal("cleanup removed replacement")
					}
					if err := os.Remove(foreign); err != nil {
						t.Fatal(err)
					}
					if err := os.Remove(receipt.name); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(backup, receipt.name); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := s.finishInput(context.Background(), input, true); err == nil || !s.m.CopiesPending || writes != 0 {
				t.Fatal("cleanup failure acknowledged completion")
			}
			restore()
			if err := s.recoverInputFinish(context.Background(), finishBegin(input)); err != nil || s.m.CopiesPending || writes != 1 {
				t.Fatal("owned Finish cleanup could not recover", err)
			}
		})
	}
}
func TestLateInputFinishRejectsRemoteOnlyChangedAndForeignReceipts(t *testing.T) {
	s, input := inputFinishFixture(t)
	old := saveManifest
	t.Cleanup(func() { saveManifest = old })
	writes := 0
	saveManifest = func(*host.Manifest) error { writes++; return nil }
	for _, change := range []func(*inputtransfer.BeginArgs){func(a *inputtransfer.BeginArgs) { a.RunGeneration = "other" }, func(a *inputtransfer.BeginArgs) { a.Nonce = api.InputNonce(strings.Repeat("c", 64)) }, func(a *inputtransfer.BeginArgs) { a.Digest = strings.Repeat("c", 64) }, func(a *inputtransfer.BeginArgs) { a.Summary.CopyBytes = 1 }} {
		begin := finishBegin(input)
		change(&begin)
		if err := s.recoverInputFinish(context.Background(), begin); err == nil {
			t.Fatal("foreign or changed receipt finished input")
		}
	}
	input.completion.senderDone = false
	if err := s.recoverInputFinish(context.Background(), finishBegin(input)); err == nil || writes != 0 || !s.m.CopiesPending {
		t.Fatal("remote completion selected an unverified input")
	}
}

func TestDescribeInputOperationReturnsOnlyExactOwnedCurrentSummary(t *testing.T) {
	s, input := inputFinishFixture(t)
	request := inputtransfer.OperationArgs{Scope: input.scope, RunGeneration: input.runGeneration}
	raw, _ := json.Marshal(request)
	result, err := s.describeInputOperation(raw)
	if err != nil || result.Version != 1 || result.RunGeneration != input.runGeneration || !result.Summary.Equal(input.summary) {
		t.Fatal("exact protected operation summary unavailable", err)
	}
	result.Summary.CopyBytes = 1
	if input.summary.CopyBytes != 0 || s.m.Inputs.CopyBytes != 0 {
		t.Fatal("describe result aliases protected summary")
	}
	for _, mutate := range []func(*inputtransfer.OperationArgs){func(a *inputtransfer.OperationArgs) { a.RunGeneration = "other-run" }, func(a *inputtransfer.OperationArgs) { a.Nonce = api.InputNonce(strings.Repeat("c", 64)) }, func(a *inputtransfer.OperationArgs) { a.Digest = strings.Repeat("c", 64) }} {
		changed := request
		mutate(&changed)
		bad, _ := json.Marshal(changed)
		if _, err := s.describeInputOperation(bad); err == nil {
			t.Fatal("foreign operation described current input")
		}
	}
	s.lateInput = nil
	if _, err := s.describeInputOperation(raw); err == nil {
		t.Fatal("remote row without retained operation supplied a summary")
	}
}
func TestFinishBeginRequiresOriginalRunSummaryAndRejectsAliasedOrNullFields(t *testing.T) {
	_, input := inputFinishFixture(t)
	raw, _ := json.Marshal(finishBegin(input))
	if _, err := decodeInputBegin(raw); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"finish_only":true`, `"finish_only":null`, 1),
		strings.Replace(string(raw), `"finish_only":true`, `"Finish_Only":true`, 1),
		strings.Replace(string(raw), `"run_generation":"original-run"`, `"run_generation":null`, 1),
		strings.Replace(string(raw), `"run_generation":"original-run"`, `"run_generation":""`, 1),
		strings.Replace(string(raw), `"summary":{`, `"summary":null,"unused":{`, 1),
	} {
		if _, err := decodeInputBegin([]byte(bad)); err == nil {
			t.Fatal("invalid finish metadata accepted")
		}
	}
}
