//go:build linux && amd64

package host

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

// This exercises a real original OP/OFD and protected file comparison only.
// Metadata fixture bytes are not executable artifacts or a VM/native grant.
func TestResumeImageUsesSameProtectedNeedAndIgnoresAdvisoryRow(t *testing.T) {
	secretHome(t)
	need := resumeImageMetadataFixtureV1()
	encoded, err := vmneed.EncodeV1(need, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	expected, err := vmneed.EncodeImageV1(need.Image)
	if err != nil {
		t.Fatal(err)
	}
	budget, err := vmneed.BudgetFor(need.GuestRAM)
	if err != nil {
		t.Fatal(err)
	}
	id := "image234"
	saved := Manifest{Schema: 2, ID: id, Name: "image", Wall: &WallRecord{Level: "microvm", Driver: "chv"}, VMNeedV1: encoded, RuntimeManifest: &need.Runtime, Limits: Limits{MemoryMax: budget.Limit, Declared: budget.Limit}}
	if err := paths.EnsurePrivateDir(paths.SessionDir(id)); err != nil {
		t.Fatal(err)
	}
	write := func() {
		t.Helper()
		raw, err := saved.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(paths.SessionDir(id), "manifest.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	original, err := BeginSessionOperation(context.Background(), id, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	if err := checkResumeExpectedImageV1(context.Background(), original, expected); err != nil {
		t.Fatal(err)
	}
	// A changed host display projection cannot choose a new protected image.
	saved.Run.Image = "sha256:" + strings.Repeat("9", 64)
	write()
	if err := checkResumeExpectedImageV1(context.Background(), original, expected); err != nil {
		t.Fatal(err)
	}
	replacement := need.Image.Clone()
	replacement.Digest = "sha256:" + strings.Repeat("8", 64)
	other, err := vmneed.EncodeImageV1(replacement)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkResumeExpectedImageV1(context.Background(), original, other); err == nil {
		t.Fatal("advisory replacement changed original protected image")
	}
	// Offline restore receives no new laptop policy. No expected-image field
	// may manufacture a policy update or change the retained original Need.
	if err := checkResumeExpectedImageV1(context.Background(), original, nil); err != nil {
		t.Fatal(err)
	}
	current, err := original.LoadContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current.VMNeedV1, encoded) {
		t.Fatal("image comparison rewrote original Need")
	}
	if err := checkResumeExpectedImageV1(context.Background(), &SessionOperation{id: id}, expected); err == nil {
		t.Fatal("data-only operation supplied protected source")
	}
}
