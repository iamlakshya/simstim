package host_test

import (
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

func TestEmptyRenameAlwaysExplainsNextStep(t *testing.T) {
	hosttest.New(t)
	for _, name := range []string{"", "  "} {
		if err := host.Rename("abcd2345", name); codeOf(err) != "bad_name" || hintOf(err) == "" {
			t.Errorf("empty direct rename has no next step: %v", err)
		}
	}
	s, err := host.New(host.NewArgs{Name: "before", Cwd: "~", Argv: []string{"sleep", "60"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	_, err = host.HolderRequest(s.ID, "rename", map[string]string{"name": "  "}, time.Second)
	if err == nil || hintOf(err) == "" {
		t.Errorf("empty holder rename has no next step: %v", err)
	}
}
