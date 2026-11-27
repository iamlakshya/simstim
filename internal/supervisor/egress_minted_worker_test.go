package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

func configureMintedFixture(t *testing.T, f *eventFixture) egress.MintedStateEvent {
	t.Helper()
	state, event := validPublicMintFixture()
	f.s.saveMu.Lock()
	f.s.mu.Lock()
	f.s.m.SecretState = state
	f.s.firstSaved = true
	f.c.owner.caCert = "public-ca-fixture"
	initial := egress.InitArgs{SessionID: f.s.id, SessionRef: sessionRefOf(f.s.m), Policy: api.ClonePolicy(*f.s.m.Policy)}
	f.s.mu.Unlock()
	f.s.saveMu.Unlock()
	f.c.setPublicInitial(initial)
	return event
}

func replaceMintedSave(t *testing.T, f *eventFixture, save func(*host.Manifest) error, release func()) {
	t.Helper()
	previous := saveManifest
	saveManifest = save
	t.Cleanup(func() {
		if release != nil {
			release()
		}
		_ = f.local.Close()
		waitEventSignal(t, f.c.minted.finished, "mint worker before restoring save hook")
		saveManifest = previous
	})
}

func mintedAck(t *testing.T, f *eventFixture) (proto.Request, egress.SecretSavedArgs) {
	t.Helper()
	request := f.next(t)
	var ack egress.SecretSavedArgs
	if request.Op != egress.CtlSecretSaved || json.Unmarshal(request.Args, &ack) != nil {
		t.Fatal("expected a correlated secret.saved ACK")
	}
	return request, ack
}

func sameMintedReceipt(event egress.MintedStateEvent, ack egress.SecretSavedArgs) bool {
	return ack.Version == event.Version && ack.Generation == event.Generation && ack.Receipt == event.Receipt && ack.Owner == event.Owner && ack.Identity == event.State.Identity
}

func TestMintedPersistencePrecedesACKWithoutPolMuOrReplyReaderDeadlock(t *testing.T) {
	f := eventController(t)
	event := configureMintedFixture(t, f)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	previous := saveManifest
	replaceMintedSave(t, f, func(target *host.Manifest) error {
		close(entered)
		<-release
		return previous(target)
	}, unblock)
	f.s.polMu.Lock()
	var releasePolicy sync.Once
	unlock := func() { releasePolicy.Do(f.s.polMu.Unlock) }
	t.Cleanup(unlock)
	f.event(t, egress.CtlEvent{Event: egress.EventSecretMinted, Minted: &event})
	waitEventSignal(t, entered, "minted save while polMu is held")
	// A real following RPC is a frame-order barrier. A premature ACK would
	// precede it on the same writer and fail the exact operation assertion.
	replied := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		replied <- f.c.request(ctx, "while-save-blocked", nil, nil)
	}()
	barrier := f.next(t)
	if barrier.Op != "while-save-blocked" {
		t.Fatal("secret.saved was emitted before durable storage completed")
	}
	f.reply(t, barrier)
	select {
	case err := <-replied:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("storage I/O blocked the sole control reply reader")
	}
	unblock()
	request, ack := mintedAck(t, f)
	if !ack.Durable || !sameMintedReceipt(event, ack) {
		t.Fatal("durable ACK lost its exact event labels")
	}
	stored, err := host.LoadManifest(f.s.id)
	if err != nil || stored.SecretState == nil || len(stored.SecretState.Minted[event.Owner]) != 1 || !samePublicMintedState(stored.SecretState.Minted[event.Owner][0], event.State) {
		t.Fatal("durable ACK preceded the actual public manifest record")
	}
	f.reply(t, request)
	unlock()
}

func TestFailedMintedSaveSendsNegativeACKWithoutPublishingOrGlobalClosure(t *testing.T) {
	f := eventController(t)
	event := configureMintedFixture(t, f)
	replaceMintedSave(t, f, func(*host.Manifest) error { return errors.New("private-storage-marker") }, nil)
	f.event(t, egress.CtlEvent{Event: egress.EventSecretMinted, Minted: &event})
	request, ack := mintedAck(t, f)
	if ack.Durable || !sameMintedReceipt(event, ack) {
		t.Fatal("failed storage produced success or lost the failing receipt")
	}
	f.s.mu.Lock()
	count := len(f.s.m.SecretState.Minted[event.Owner])
	f.s.mu.Unlock()
	if count != 0 {
		t.Fatal("failed durable commit published live minted metadata")
	}
	f.reply(t, request)
	select {
	case <-f.local.Done():
		t.Fatal("negative durable ACK closed unrelated control/flows")
	default:
	}
}

func TestMintedRetrySavesOnceAndEchoesEachReceiptGeneration(t *testing.T) {
	f := eventController(t)
	event := configureMintedFixture(t, f)
	for i := range 2 {
		current := event
		if i == 1 {
			current.Generation, current.Receipt = strings.Repeat("f", 32), strings.Repeat("1", 32)
		}
		f.event(t, egress.CtlEvent{Event: egress.EventSecretMinted, Minted: &current})
		request, ack := mintedAck(t, f)
		if !ack.Durable || !sameMintedReceipt(current, ack) {
			t.Fatal("retry lost its own correlated ACK")
		}
		f.reply(t, request)
	}
	stored, err := host.LoadManifest(f.s.id)
	if err != nil || stored.SecretState == nil || len(stored.SecretState.Minted[event.Owner]) != 1 || stored.SecretState.Generation != strings.Repeat("c", 32) {
		t.Fatal("receipt retry duplicated a stand-in or rolled back the public epoch")
	}
}

func TestOwnerReplacementDuringMintedSaveCannotPublishOrACK(t *testing.T) {
	f := eventController(t)
	event := configureMintedFixture(t, f)
	previous := saveManifest
	replaceMintedSave(t, f, func(target *host.Manifest) error {
		f.s.mu.Lock()
		f.s.eg = &egressProc{}
		f.s.mu.Unlock()
		return previous(target)
	}, nil)
	if err := f.c.persistMinted(context.Background(), event); err == nil {
		t.Fatal("old owner confirmed a public record after replacement")
	}
	if f.c.currentMintedRecord(context.Background(), event) {
		t.Fatal("old owner can ACK replacement state")
	}
	f.s.mu.Lock()
	count := len(f.s.m.SecretState.Minted[event.Owner])
	f.s.mu.Unlock()
	if count != 0 {
		t.Fatal("old owner published into replacement live state")
	}
}

func TestDeadMintedOwnerCannotCommitPublicState(t *testing.T) {
	f := eventController(t)
	event := configureMintedFixture(t, f)
	f.c.owner.dead.Store(true)
	if err := f.c.persistMinted(context.Background(), event); err == nil {
		t.Fatal("dead egress owner committed public state")
	}
}

func TestMissingTrustedInitialProjectionCannotClaimDurableMintedState(t *testing.T) {
	f := eventController(t)
	event := configureMintedFixture(t, f)
	f.c.mu.Lock()
	f.c.publicInitReady = false
	f.c.mu.Unlock()
	if err := f.c.persistMinted(context.Background(), event); err == nil {
		t.Fatal("missing actual initial metadata was replaced by guessed kinds")
	}
}

func TestMissingActualPublicCACannotClaimCompleteStateBudget(t *testing.T) {
	f := eventController(t)
	event := configureMintedFixture(t, f)
	f.c.owner.caCert = ""
	if err := f.c.persistMinted(context.Background(), event); err == nil {
		t.Fatal("missing actual public CA was replaced by a guessed frame projection")
	}
}

func TestQueuedOldSnapshotCannotEraseAcknowledgedMintedState(t *testing.T) {
	f := eventController(t)
	event := configureMintedFixture(t, f)
	f.s.mu.Lock()
	old := f.s.snapshot()
	f.s.mu.Unlock()
	if err := f.c.persistMinted(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := f.s.save(&old, false); err != nil {
		t.Fatal(err)
	}
	stored, err := host.LoadManifest(f.s.id)
	if err != nil || stored.SecretState == nil || len(stored.SecretState.Minted[event.Owner]) != 1 {
		t.Fatal("a pre-mint detached snapshot overwrote the durable public record")
	}
}

func TestReorderedMintedReceiptsPreserveAllRecordsAndAuthorityGeneration(t *testing.T) {
	f := eventController(t)
	first := configureMintedFixture(t, f)
	second := first
	second.Receipt = strings.Repeat("2", 32)
	second.State.Identity = strings.Repeat("3", 32)
	second.State.Sentinel = api.Secret("sk-" + strings.Repeat("C", 20) + "T3BlbkFJ" + strings.Repeat("D", 20))
	for _, event := range []egress.MintedStateEvent{second, first} {
		if err := f.c.persistMinted(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate an independently committed newer primary authority snapshot
	// through the SAME writer. A reordered old receipt cannot roll it back.
	f.s.saveMu.Lock()
	f.s.mu.Lock()
	f.s.m.SecretState.Generation = strings.Repeat("f", 32)
	f.s.mu.Unlock()
	var snapshot host.Manifest
	err := f.s.writeManifestChange(&snapshot, false, nil, nil, nil, nil)
	f.s.saveMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	first.Receipt = strings.Repeat("4", 32)
	if err := f.c.persistMinted(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	stored, err := host.LoadManifest(f.s.id)
	if err != nil || stored.SecretState == nil || stored.SecretState.Generation != strings.Repeat("f", 32) || len(stored.SecretState.Minted[first.Owner]) != 2 {
		t.Fatal("old or reordered receipt erased a mint or rolled authority backwards")
	}
}

func TestRetiredMintedBindingRefusesBeforeStorage(t *testing.T) {
	f := eventController(t)
	event := configureMintedFixture(t, f)
	f.s.mu.Lock()
	binding := f.s.m.SecretState.Intent.Binds[event.Owner]
	binding.Hosts = []string{"other.example:443"}
	f.s.m.SecretState.Intent.Binds[event.Owner] = binding
	f.s.mu.Unlock()
	if err := f.c.persistMinted(context.Background(), event); err == nil {
		t.Fatal("receipt from a retired binding was persisted")
	}
}

func TestMalformedMintedMetadataIsLocalRefusalAndPreservesReplyReader(t *testing.T) {
	for _, mutation := range []string{"state unknown", "identity alias", "duplicate receipt", "slot alias"} {
		t.Run(mutation, func(t *testing.T) {
			f := eventController(t)
			event := configureMintedFixture(t, f)
			raw, err := json.Marshal(egress.CtlEvent{Event: egress.EventSecretMinted, Minted: &event})
			if err != nil {
				t.Fatal(err)
			}
			body := string(raw)
			switch mutation {
			case "state unknown":
				body = strings.Replace(body, `"state":{`, `"state":{"future":true,`, 1)
			case "identity alias":
				body = strings.Replace(body, `"identity":`, `"Identity":`, 1)
			case "duplicate receipt":
				body = strings.Replace(body, `"receipt":`, `"receipt":"other","receipt":`, 1)
			case "slot alias":
				body = strings.Replace(body, `"class":`, `"Class":`, 1)
			}
			if body == string(raw) || !json.Valid([]byte(body)) {
				t.Fatal("invalid structural mutation fixture")
			}
			if err := f.writer.Write(proto.TEvent, []byte(body)); err != nil {
				t.Fatal(err)
			}
			// The response to this ordinary request follows the malformed
			// event on the same incoming writer. Completion fences parsing.
			result := make(chan error, 1)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				result <- f.c.request(ctx, "after-malformed", nil, nil)
			}()
			request := f.next(t)
			if request.Op != "after-malformed" {
				t.Fatal("malformed metadata produced a durable ACK")
			}
			f.reply(t, request)
			select {
			case err := <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("malformed receipt stopped an unrelated RPC")
			}
			f.c.minted.mu.Lock()
			pending := len(f.c.minted.order)
			f.c.minted.mu.Unlock()
			f.s.mu.Lock()
			published := len(f.s.m.SecretState.Minted[event.Owner])
			f.s.mu.Unlock()
			if pending != 0 || published != 0 {
				t.Fatal("unsupported nested structure entered queue or public state")
			}
		})
	}
}

func TestMintedEOFWhileWaitingForWriterCannotCommit(t *testing.T) {
	f := eventController(t)
	event := configureMintedFixture(t, f)
	f.s.saveMu.Lock()
	var once sync.Once
	unlock := func() { once.Do(f.s.saveMu.Unlock) }
	started, completed := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		defer close(completed)
		close(started)
		result <- f.c.persistMinted(context.Background(), event)
	}()
	t.Cleanup(func() { unlock(); waitEventSignal(t, completed, "owned persistence call") })
	waitEventSignal(t, started, "persistence waiting for writer")
	_ = f.peer.Close()
	waitEventSignal(t, f.c.done, "owned control EOF")
	unlock()
	waitEventSignal(t, completed, "persistence after EOF")
	if err := <-result; err == nil {
		t.Fatal("EOF during writer wait allowed a durable public commit")
	}
	f.s.mu.Lock()
	count := len(f.s.m.SecretState.Minted[event.Owner])
	f.s.mu.Unlock()
	if count != 0 {
		t.Fatal("an asynchronous disarmer masked published minted state")
	}
}
