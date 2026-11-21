package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The participant keeps a real original file descriptor across staging,
// rollback and finalisation. It makes no host/provider/network operation.
type savedRemovalParticipantFixtureV1 struct {
	preparation                        *savedHostRemovalPreparationV1
	root                               string
	file                               *os.File
	events                             []string
	stageErr, rollbackErr, finaliseErr error
	stagePhase                         *savedHostRemovalStageV1
	commitPhase                        *savedHostRemovalCommitV1
	afterStage                         func()
}

func (p *savedRemovalParticipantFixtureV1) stage(ctx context.Context, phase *savedHostRemovalStageV1) error {
	if err := phase.CheckParticipant(ctx, p.preparation); err != nil {
		return err
	}
	copy := &savedHostRemovalPreparationV1{p.preparation.savedHostRemovalPhaseV1}
	if phase.CheckParticipant(ctx, copy) == nil {
		return errors.New("a copied preparation object gained native authority")
	}
	if _, err := FindHost(phase.Host().Name); err != nil {
		return err
	}
	p.events = append(p.events, "stage")
	p.stagePhase = phase
	if err := os.Rename(filepath.Join(p.root, "active"), filepath.Join(p.root, "retired")); err != nil {
		return err
	}
	if p.afterStage != nil {
		p.afterStage()
	}
	return p.stageErr
}
func (p *savedRemovalParticipantFixtureV1) rollback(ctx context.Context, phase *savedHostRemovalRollbackV1) error {
	if err := phase.CheckParticipant(ctx, p.preparation); err != nil {
		return err
	}
	p.events = append(p.events, "rollback")
	if p.rollbackErr != nil {
		return p.rollbackErr
	}
	if _, err := os.Stat(filepath.Join(p.root, "retired")); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return os.Rename(filepath.Join(p.root, "retired"), filepath.Join(p.root, "active"))
}
func (p *savedRemovalParticipantFixtureV1) finalise(ctx context.Context, phase *savedHostRemovalCommitV1) error {
	if err := phase.CheckParticipant(ctx, p.preparation); err != nil {
		return err
	}
	if _, err := FindHost(phase.Host().Name); err == nil {
		return errors.New("finalisation ran before the actual forget")
	}
	p.events = append(p.events, "finalise")
	p.commitPhase = phase
	if p.finaliseErr != nil {
		return p.finaliseErr
	}
	return os.Remove(filepath.Join(p.root, "retired"))
}
func (p *savedRemovalParticipantFixtureV1) close() error {
	p.events = append(p.events, "close")
	if p.file == nil {
		return nil
	}
	err := p.file.Close()
	p.file = nil
	return err
}

func savedRemovalFixtureV1(t *testing.T) (*SavedHostRemovalV1, *savedRemovalParticipantFixtureV1) {
	t.Helper()
	host := Host{Name: "joined", SSH: "builder.example"}
	prepareSavedFixture(t, host, TierShared, strings.Repeat("b", 32), false)
	participant := &savedRemovalParticipantFixtureV1{root: t.TempDir()}
	if err := os.WriteFile(filepath.Join(participant.root, "active"), []byte("original owned configuration"), 0600); err != nil {
		t.Fatal(err)
	}
	factory := savedHostRemovalFactoryV1{name: "native-fixture", open: func(ctx context.Context, phase *savedHostRemovalPreparationV1) (savedHostRemovalParticipantV1, error) {
		if err := phase.Check(ctx); err != nil {
			return nil, err
		}
		if err := phase.CheckParticipant(ctx, phase); err != nil {
			return nil, err
		}
		wrongStage := &savedHostRemovalStageV1{phase.savedHostRemovalPhaseV1}
		wrongRollback := &savedHostRemovalRollbackV1{phase.savedHostRemovalPhaseV1}
		wrongCommit := &savedHostRemovalCommitV1{phase.savedHostRemovalPhaseV1}
		if wrongStage.Check(ctx) == nil || wrongRollback.Check(ctx) == nil || wrongCommit.Check(ctx) == nil {
			return nil, errors.New("preparation donated another native phase")
		}
		foreign := NewRootStepGrantRequest()
		mixed := phase.Check(WithGrantRequest(ctx, foreign))
		closeErr := foreign.Close()
		if mixed == nil || closeErr != nil {
			return nil, errors.New("native removal donated another grant purpose")
		}
		participant.events = append(participant.events, "prepare")
		participant.preparation = phase
		file, err := os.Open(filepath.Join(participant.root, "active"))
		if errors.Is(err, os.ErrNotExist) {
			file, err = os.Open(filepath.Join(participant.root, "retired"))
		}
		if err != nil {
			return nil, err
		}
		participant.file = file
		return participant, nil
	}}
	removal, err := openSavedHostRemovalV1(t.Context(), host.Name, []savedHostRemovalFactoryV1{factory})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		participant.stageErr, participant.rollbackErr, participant.finaliseErr = nil, nil, nil
		ctx := context.WithoutCancel(removal.Context())
		if removal.attempt != nil && !removal.attempt.finished {
			if removal.attempt.committed {
				_ = removal.Retry(ctx)
			} else {
				_ = removal.Rollback(ctx)
			}
		}
		if err := removal.Close(); err != nil {
			// A refused original Source is expected in replacement tests. Only
			// this fixture owns these retained FDs and its temporary OP.
			_ = participant.close()
			removal.release()
		}
	})
	return removal, participant
}

func TestSavedHostRemovalOrdinaryHostUsesOneSourceAndActualWriter(t *testing.T) {
	removal, participant := savedRemovalFixtureV1(t)
	if removal.Host().Machine != nil || removal.HostID() != strings.Repeat("b", 32) {
		t.Fatal("ordinary host removal acquired a machine requirement")
	}
	if err := removal.Forget(removal.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := FindHost(removal.Host().Name); err == nil {
		t.Fatal("actual canonical host was not forgotten")
	}
	if got, want := participant.events, []string{"prepare", "stage", "finalise"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events=%v", got)
	}
	if participant.preparation.Check(participant.preparation.Context()) == nil || participant.stagePhase.Check(participant.stagePhase.Context()) == nil || participant.commitPhase.Check(participant.commitPhase.Context()) == nil {
		t.Fatal("an ended callback retained native permission")
	}
	if err := removal.Close(); err != nil {
		t.Fatal(err)
	}
	if participant.file != nil {
		t.Fatal("original native descriptor was not closed")
	}
	if err := removal.Check(removal.Context()); err == nil {
		t.Fatal("closed removal retained its original Source")
	}
}

func TestSavedHostRemovalFailedWriterRollsBackSameInodeAndRetriesSameParticipant(t *testing.T) {
	removal, participant := savedRemovalFixtureV1(t)
	before, err := os.Stat(filepath.Join(participant.root, "active"))
	if err != nil {
		t.Fatal(err)
	}
	original := writeHostsSelectedGeneration
	t.Cleanup(func() { writeHostsSelectedGeneration = original })
	failure := errors.New("generated canonical write failure")
	writeHostsSelectedGeneration = func([]Host, string) error { return failure }
	if err := removal.Forget(removal.Context()); !errors.Is(err, failure) {
		t.Fatalf("forget error=%v", err)
	}
	writeHostsSelectedGeneration = original
	after, err := os.Stat(filepath.Join(participant.root, "active"))
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("rollback replaced the original owned inode")
	}
	if _, err := FindHost(removal.Host().Name); err != nil {
		t.Fatal("failed forget lost the saved original")
	}
	if err := removal.Retry(removal.Context()); err != nil {
		t.Fatal(err)
	}
	if got, want := participant.events, []string{"prepare", "stage", "rollback", "stage", "finalise"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("retry reconstructed its participant: %v", got)
	}
}

func TestSavedHostRemovalPartialStageFailureStillRollsBackOriginal(t *testing.T) {
	removal, participant := savedRemovalFixtureV1(t)
	participant.stageErr = errors.New("staged native failure")
	if err := removal.Forget(removal.Context()); !errors.Is(err, participant.stageErr) {
		t.Fatalf("stage failure=%v", err)
	}
	if _, err := os.Stat(filepath.Join(participant.root, "active")); err != nil {
		t.Fatal("partial stage did not restore its original file")
	}
	if got, want := participant.events, []string{"prepare", "stage", "rollback"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events=%v", got)
	}
	if _, err := FindHost(removal.Host().Name); err != nil {
		t.Fatal("partial native stage forgot the host")
	}
}

func TestSavedHostRemovalCloseRollsBackInheritedNativeCustody(t *testing.T) {
	removal, participant := savedRemovalFixtureV1(t)
	before, err := os.Stat(filepath.Join(participant.root, "active"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(participant.root, "active"), filepath.Join(participant.root, "retired")); err != nil {
		t.Fatal(err)
	}
	if err := removal.Prepare(removal.Context()); err != nil {
		t.Fatal(err)
	}
	if removal.participants[0].staged {
		t.Fatal("the new producer claimed an earlier process's stage")
	}
	if err := removal.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(filepath.Join(participant.root, "active"))
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("close lost the original inherited inode")
	}
	if _, err := FindHost(removal.Host().Name); err != nil {
		t.Fatal("closing recovered native custody forgot the host")
	}
	if got, want := participant.events, []string{"prepare", "rollback", "close"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("inherited custody events=%v", got)
	}
}

func TestSavedHostRemovalCancellationRollsBackUnderOriginalSource(t *testing.T) {
	removal, participant := savedRemovalFixtureV1(t)
	ctx, cancel := context.WithCancel(removal.Context())
	defer cancel()
	participant.afterStage = cancel
	if err := removal.Forget(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled forget = %v", err)
	}
	if _, err := os.Stat(filepath.Join(participant.root, "active")); err != nil {
		t.Fatal("cancellation discarded the original staged inode")
	}
	if _, err := FindHost(removal.Host().Name); err != nil {
		t.Fatal("cancellation forgot the saved host")
	}
	if err := removal.Close(); err != nil {
		t.Fatalf("completed original rollback retained the host operation: %v", err)
	}
}

func TestSavedHostRemovalFinaliseFailureRetainsActualCommitAndHotRetry(t *testing.T) {
	removal, participant := savedRemovalFixtureV1(t)
	participant.finaliseErr = errors.New("native finalisation held")
	if err := removal.Forget(removal.Context()); !errors.Is(err, participant.finaliseErr) {
		t.Fatalf("finalise failure=%v", err)
	}
	if err := removal.Close(); err == nil {
		t.Fatal("unfinished native finalisation released the original OP")
	}
	if _, err := OpenSavedHostRemovalV1(t.Context(), removal.Host().Name); err == nil {
		t.Fatal("absence metadata reconstructed a new native deletion owner")
	}
	participant.finaliseErr = nil
	if err := removal.Retry(removal.Context()); err != nil {
		t.Fatal(err)
	}
	if got, want := participant.events, []string{"prepare", "stage", "finalise", "finalise"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("hot retry reopened or restaged its owner: %v", got)
	}
	if err := removal.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSavedHostRemovalRejectsCopiedContextAndOneProducerPerActualOP(t *testing.T) {
	removal, _ := savedRemovalFixtureV1(t)
	if _, err := OpenSavedHostRemovalV1(removal.Context(), removal.Host().Name); err == nil {
		t.Fatal("a nested context minted another removal")
	}
	bare := context.WithValue(t.Context(), heldKey{removal.Host().Name}, removal.op)
	if _, err := OpenSavedHostRemovalV1(bare, removal.Host().Name); err == nil {
		t.Fatal("the same physical OP minted a second producer")
	}
	if err := removal.Check(t.Context()); err == nil {
		t.Fatal("a context without the actual OP borrowed its Source")
	}
}

func TestSavedHostRemovalZeroValueCannotCreateOrCloseAnOperation(t *testing.T) {
	var removal SavedHostRemovalV1
	for _, run := range []func(context.Context) error{removal.Check, removal.Prepare, removal.Forget, removal.Rollback, removal.Retry} {
		if err := run(t.Context()); err == nil {
			t.Fatal("a zero-value owner acquired a removal scope")
		}
	}
	if err := removal.Close(); err == nil {
		t.Fatal("a zero-value owner claimed native closure")
	}
	if removal.Context() != nil || removal.HostID() != "" || removal.Host().Name != "" {
		t.Fatal("zero-value reporting minted original identity")
	}
}

func TestSavedHostRemovalSourceABAFailsBeforeNativePreparation(t *testing.T) {
	removal, participant := savedRemovalFixtureV1(t)
	data, err := os.ReadFile(hostsPath())
	if err != nil {
		t.Fatal(err)
	}
	generation, err := newMembershipGeneration()
	if err != nil {
		t.Fatal(err)
	}
	old := membershipGenerationPrefix + removal.source.generation
	if !strings.Contains(string(data), old) {
		t.Fatal("fixture has no original Source marker")
	}
	data = []byte(strings.Replace(string(data), old, membershipGenerationPrefix+generation, 1))
	if err := os.WriteFile(hostsPath(), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := removal.Forget(removal.Context()); err == nil {
		t.Fatal("identical-row replacement retained old removal permission")
	}
	if len(participant.events) != 0 {
		t.Fatal("a replaced Source reached the native participant")
	}
	if _, err := FindHost(removal.Host().Name); err != nil {
		t.Fatal("stale removal forgot the replacement")
	}
}

func TestSavedHostRemovalPostCommitReplacementRevokesRetainedFinalisation(t *testing.T) {
	removal, participant := savedRemovalFixtureV1(t)
	participant.finaliseErr = errors.New("native finalisation held")
	if err := removal.Forget(removal.Context()); !errors.Is(err, participant.finaliseErr) {
		t.Fatal(err)
	}
	data, err := os.ReadFile(hostsPath())
	if err != nil {
		t.Fatal(err)
	}
	generation, err := newMembershipGeneration()
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), membershipGenerationPrefix+removal.attempt.post.generation, membershipGenerationPrefix+generation, 1))
	if err := os.WriteFile(hostsPath(), data, 0600); err != nil {
		t.Fatal(err)
	}
	before := append([]string(nil), participant.events...)
	if err := removal.Retry(removal.Context()); err == nil {
		t.Fatal("new absent Source reconstructed original committed native authority")
	}
	if !reflect.DeepEqual(participant.events, before) {
		t.Fatal("a changed committed Source reached native unlink")
	}
}
