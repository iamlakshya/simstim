//go:build linux

package supervisor

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestOriginalPopularityStartupFactoryCapturesBeforeEgressExists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rank.csv")
	original := []byte("rank,domain\n12,example.com\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	policy := api.Policy{LayerValues: api.LayerValues{Classifier: api.ClassifierValues{Popularity: &path}}}
	prepared, err := prepareClassifierPopularityV1(t.Context(), policy)
	if err != nil || prepared == nil {
		t.Fatal("actual original factory did not capture its configured file", err)
	}
	defer prepared.Close()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	link, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer link.Close()
	cmd := exec.Command("/unused/helper", "egress", "bound234")
	cmd.ExtraFiles = []*os.File{link}
	if err := prepared.AppendEgressCommandV1(t.Context(), cmd, "bound234"); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, len(original))
	if _, err := cmd.ExtraFiles[1].ReadAt(data, 0); err != nil || string(data) != string(original) {
		t.Fatal("factory handed off a path or replacement instead of its original file", err)
	}
}

func TestPopularityStartupCannotFollowAReplacementLeafLink(t *testing.T) {
	root := t.TempDir()
	target, selected := filepath.Join(root, "actual.csv"), filepath.Join(root, "selected.csv")
	if err := os.WriteFile(target, []byte("12,example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, selected); err != nil {
		t.Fatal(err)
	}
	policy := api.Policy{LayerValues: api.LayerValues{Classifier: api.ClassifierValues{Popularity: &selected}}}
	if prepared, err := prepareClassifierPopularityV1(t.Context(), policy); err != nil || prepared != nil {
		t.Fatal("a selected symlink issued a file source", err)
	}
}
