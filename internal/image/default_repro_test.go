package image

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultReproChecksBothActualTreesAndRejectsChangedRootContent(t *testing.T) {
	first, a, lock := prepareDefaultFixture(t, "same canonical root\n")
	second, b, _ := prepareDefaultFixture(t, "same canonical root\n")
	report, err := CompareDefaultBuilds(t.Context(), first, second, lock)
	if err != nil || report.Version != 1 || report.ImageDigest != a.Image.Digest || report.FirstRootSHA256 != a.RootSHA256 || report.SecondRootSHA256 != b.RootSHA256 {
		t.Fatal(report, err)
	}
	if _, err := CompareDefaultBuilds(t.Context(), first, first+"/.", lock); err == nil {
		t.Fatal("same native build directory counted twice")
	}
	different, _, _ := prepareDefaultFixture(t, "different canonical root\n")
	if _, err := CompareDefaultBuilds(t.Context(), first, different, lock); err == nil {
		t.Fatal("different real root layers passed reproducibility")
	}
	path := filepath.Join(second, "prepared.json")
	b.RootSHA256 = a.Image.Digest // A metadata assertion is not an actual artifact check.
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CompareDefaultBuilds(t.Context(), first, second, lock); err == nil {
		t.Fatal("tampered second artifact was trusted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CompareDefaultBuilds(ctx, first, first, lock); err == nil {
		t.Fatal("cancelled comparison succeeded")
	}
}
