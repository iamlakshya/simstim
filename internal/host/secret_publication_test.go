package host

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
)

func compactPublicationFixture() *Manifest {
	binding := api.SecretBind{Hosts: []string{"example.com:443"}, Header: "Authorization", Format: "Bearer {value}"}
	shape := &api.SecretShape{Version: 1, Slots: []api.SecretShapeSlot{{Class: "ascii-upper", Count: 4, Width: 1}, {Class: "ascii-digit", Count: 4, Width: 1}}}
	return &Manifest{ID: "abc23456", Secrets: []string{"KEY"}, Status: Status{State: StateExited},
		SecretState: &api.SessionSecretState{Version: api.SessionSecretStateV1, Generation: strings.Repeat("a", 32),
			Intent:  api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1, Modes: map[string]api.SecretMode{"KEY": api.SecretModeBound}, Binds: map[string]api.SecretBind{"KEY": binding}},
			Primary: map[string]api.PublicSecretState{"KEY": {Mode: api.SecretModeBound, Sentinel: "STND0000", Shape: shape, Identity: strings.Repeat("b", 32), Armed: true}}}}
}

func TestOrdinaryRowsKeepLargeCanonicalHistoryOutOfTheListFrame(t *testing.T) {
	m := compactPublicationFixture()
	for i := range 2000 {
		public := m.SecretState.Primary["KEY"]
		m.SecretState.Retired = ensurePublicationRetired(m.SecretState.Retired)
		m.SecretState.Retired["KEY"] = append(m.SecretState.Retired["KEY"], api.RetiredPrimarySecretState{
			Sentinel: api.Secret(fmt.Sprintf("HIST%04d", i)), Shape: public.Shape, Identity: fmt.Sprintf("%032x", i+1), Binding: m.SecretState.Intent.Binds["KEY"]})
	}
	if err := egress.ValidateSavedPublicSessionState(m.SecretState); err != nil {
		t.Fatal(err)
	}
	full, err := json.Marshal(m.SecretState)
	if err != nil || len(full)*16 <= proto.MaxFrame {
		t.Fatal("density witness does not exceed a whole list frame when repeated")
	}
	rows := make([]api.SessionInfo, 16)
	for i := range rows {
		rows[i] = infoFrom(m, false)
		if rows[i].SecretState != nil || !rows[i].SecretSummary.Supported() {
			t.Fatal("ordinary row carried history or lost its canonical mode summary")
		}
	}
	raw, err := json.Marshal(rows)
	if err != nil || len(raw) > 32<<10 || strings.Contains(string(raw), "HIST0000") || strings.Contains(string(raw), "STND0000") {
		t.Fatal("ordinary list row density still depends on retained stand-in history")
	}
	if err := proto.NewWriter(io.Discard).WriteJSON(proto.TResponse, proto.Response{ID: math.MaxUint64, OK: true, Result: raw}); err != nil {
		t.Fatal("compact ordinary list does not fit its actual response frame")
	}
}

func ensurePublicationRetired(input map[string][]api.RetiredPrimarySecretState) map[string][]api.RetiredPrimarySecretState {
	if input == nil {
		return make(map[string][]api.RetiredPrimarySecretState)
	}
	return input
}

func TestExplicitPublicationOwnsFullStateAndRefusesTheWholeOversizeResult(t *testing.T) {
	m := compactPublicationFixture()
	row := publicationInfoFrom(m, false)
	if row.SecretState == nil || row.SecretState.Primary["KEY"].Sentinel != "STND0000" || checkSecretPublicationResponse(row) != nil {
		t.Fatal("explicit start publication lost full safe state")
	}
	row.SecretState.Primary["KEY"] = api.PublicSecretState{}
	if m.SecretState.Primary["KEY"].Sentinel != "STND0000" {
		t.Fatal("publication aliases the protected manifest")
	}
	result := api.ResumeResult{Session: publicationInfoFrom(m, false), Why: strings.Repeat("x", proto.MaxFrame)}
	if err := checkSecretPublicationResponse(result); err == nil {
		t.Fatal("individually small secret state bypassed the whole result frame limit")
	}
}

func TestChildSummaryUsesCurrentRootArmingWithoutAssigningOtherCredentials(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := compactPublicationFixture()
	root.Wall = &WallRecord{Level: "process"}
	root.SecretState.Intent.Modes["OTHER"] = api.SecretModeExposed
	root.SecretState.Primary["OTHER"] = api.PublicSecretState{Mode: api.SecretModeExposed, Armed: true}
	root.SecretState.Generation = strings.Repeat("c", 32)
	public := root.SecretState.Primary["KEY"]
	public.Armed = false
	root.SecretState.Primary["KEY"] = public
	if err := root.Save(); err != nil {
		t.Fatal(err)
	}
	child := compactPublicationFixture()
	child.ID, child.SandboxOf = "def23456", root.ID
	summary := secretSummaryFrom(child)
	if !summary.Supported() || len(summary.Secrets) != 1 || summary.Secrets["KEY"].Armed || summary.Generation != root.SecretState.Generation {
		t.Fatal("child row used stale arming or inherited another member's credential")
	}
	child.SecretState = nil
	if secretSummaryFrom(child) != nil {
		t.Fatal("aggregate root metadata invented a child's absent delivery intent")
	}
}

func TestExplicitPublicationRunContextIsDistinctOwnedAndAbsentFromRows(t *testing.T) {
	root := compactPublicationFixture()
	root.Wall = &WallRecord{Level: "process"}
	root.Run = Runtime{Generation: "actual-root-run", BootID: "same-boot", StartedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	ordinary := infoFrom(root, true)
	if ordinary.RunGeneration != "" || ordinary.SecretState != nil {
		t.Fatal("ordinary row acquired publication authority")
	}
	result := publicationInfoFrom(root, true)
	if result.RunGeneration != root.Run.Generation || result.RunGeneration == result.SecretState.Generation {
		t.Fatal("run generation was missing or replaced by namespace generation")
	}
	missingBoot := CloneManifest(*root)
	missingBoot.Run.BootID = ""
	if publicationInfoFrom(&missingBoot, true).RunGeneration != "" {
		t.Fatal("root without its actual boot identity published a run receipt")
	}
	child := CloneManifest(*root)
	child.ID, child.SandboxOf = "def23456", root.ID
	if result := publicationInfoFrom(&child, true); result.RunGeneration != "" {
		t.Fatal("child without protected root proof published a run")
	}
	result = publicationInfoFrom(&child, true, root)
	if result.RunGeneration != root.Run.Generation || result.SandboxOf != root.ID {
		t.Fatal("coherent child/root context lost")
	}
	for _, change := range []func(*Manifest){func(m *Manifest) { m.Run.Generation = "different-run" }, func(m *Manifest) { m.Run.BootID = "another-boot" }, func(m *Manifest) { m.SecretState.Generation = strings.Repeat("d", 32) }, func(m *Manifest) { m.SecretState.Version = 999 }} {
		changed := CloneManifest(child)
		change(&changed)
		if publicationInfoFrom(&changed, true, root).RunGeneration != "" {
			t.Fatal("torn or unsupported child context acquired a run receipt")
		}
	}
	result.SecretState.Primary["KEY"] = api.PublicSecretState{}
	if child.SecretState.Primary["KEY"].Sentinel != "STND0000" {
		t.Fatal("publication mutated protected child state")
	}
}
