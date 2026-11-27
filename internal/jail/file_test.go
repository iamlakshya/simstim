package jail

import (
	"os"
	"strings"
	"testing"
)

func TestCheckRefusesAClosedInheritedFile(t *testing.T) {
	spec, _ := goodSpec(t)
	file, err := os.CreateTemp(t.TempDir(), "inherited")
	if err != nil {
		t.Fatal(err)
	}
	spec.Files = []*os.File{file}
	if _, err := check(spec); err != nil {
		t.Fatalf("an open inherited file was refused: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := check(spec); err == nil || !strings.Contains(err.Error(), "inherited file is closed") || hintOf(err) == "" {
		t.Errorf("a closed inherited file = %v, want a refusal with a hint", err)
	}
}
