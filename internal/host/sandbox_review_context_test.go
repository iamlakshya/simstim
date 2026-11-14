package host

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func savedReviewFixture(t *testing.T) (*Manifest, *Manifest) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := compactPublicationFixture()
	root.Schema = 1
	root.Wall = &WallRecord{Level: "process", Driver: "process"}
	root.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	root.Run = Runtime{Generation: "actual-agent-run", BootID: "actual-boot", StartedAt: root.CreatedAt}
	root.Status = Status{State: StateStopped}
	root.Life = &Life{Phase: LifeStopped, Started: root.CreatedAt, End: &LifeEnd{Kind: EndStop, At: root.CreatedAt.Add(time.Hour)}}
	if err := root.Save(); err != nil {
		t.Fatal(err)
	}
	child := CloneManifest(*root)
	child.ID, child.SandboxOf = "def23456", root.ID
	if err := child.Save(); err != nil {
		t.Fatal(err)
	}
	return root, &child
}
func TestStoppedReviewReadsSavedAppliedContextWithoutAnyAuxiliaryBoot(t *testing.T) {
	root, child := savedReviewFixture(t)
	old := stoppedGuestOpener
	t.Cleanup(func() { stoppedGuestOpener = old })
	opens := 0
	stoppedGuestOpener = func(context.Context, *SessionOperation) (StoppedGuest, error) {
		opens++
		return nil, errors.New("no guest may open for saved review")
	}
	for _, id := range []string{root.ID, child.ID} {
		view, err := SavedSandboxReviewContext(context.Background(), id)
		if err != nil || view.Snapshot.Primary != root.ID || view.RunGeneration != root.Run.Generation || view.SecretState.Generation != root.SecretState.Generation {
			t.Fatal("positive stopped saved restriction failed", err)
		}
		if view.RunGeneration == view.SecretState.Generation {
			t.Fatal("namespace generation replaced actual agent run")
		}
		view.SecretState.Primary["KEY"] = api.PublicSecretState{}
	}
	if opens != 0 {
		t.Fatal("saved context booted an auxiliary guest")
	}
	current, err := LoadManifest(root.ID)
	if err != nil || !sameSavedPublication(root, current) || current.Life.Phase != LifeStopped {
		t.Fatal("saved review mutated actual applied context", err)
	}
}
func TestStoppedReviewQuarantinesMissingFutureWrongRunAndRoot(t *testing.T) {
	for _, kind := range []string{"missing-run", "missing-boot", "future-state", "child-run", "child-root"} {
		t.Run(kind, func(t *testing.T) {
			root, child := savedReviewFixture(t)
			target := child.ID
			switch kind {
			case "missing-run":
				root.Run.Generation = ""
				if err := root.Save(); err != nil {
					t.Fatal(err)
				}
			case "missing-boot":
				root.Run.BootID = ""
				if err := root.Save(); err != nil {
					t.Fatal(err)
				}
			case "future-state":
				root.SecretState.Version = 999
				if err := root.Save(); err != nil {
					t.Fatal(err)
				}
			case "child-run":
				child.Run.Generation = "another-agent-run"
				if err := child.Save(); err != nil {
					t.Fatal(err)
				}
			case "child-root":
				child.SandboxOf = child.ID
				if err := child.Save(); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := SavedSandboxReviewContext(context.Background(), target); err == nil {
				t.Fatal("unsupported or wrong saved publication context accepted")
			}
		})
	}
}
func TestSavedPublicationComparisonIncludesFullCanonicalState(t *testing.T) {
	root, _ := savedReviewFixture(t)
	changed := CloneManifest(*root)
	public := changed.SecretState.Primary["KEY"]
	public.Armed = !public.Armed
	changed.SecretState.Primary["KEY"] = public
	if sameSavedPublication(root, &changed) {
		t.Fatal("unchanged run/generation concealed changed saved state")
	}
}
