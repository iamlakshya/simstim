//go:build linux

package supervisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/wall"
)

type gvisorLayoutWitness struct {
	prepare, open int
	owner         context.Context
	refusal       error
}

func (*gvisorLayoutWitness) Name() string                     { return "gvisor" }
func (*gvisorLayoutWitness) Level() wall.Level                { return wall.GVisor }
func (*gvisorLayoutWitness) Probe(context.Context) wall.Probe { panic("a layout read cannot probe") }
func (w *gvisorLayoutWitness) Prepare(ctx context.Context, _ wall.Spec) (wall.Sandbox, error) {
	w.prepare++
	w.owner = ctx
	return nil, w.refusal
}
func (w *gvisorLayoutWitness) Open(ctx context.Context, _ string) (wall.Sandbox, error) {
	w.open++
	w.owner = ctx
	return nil, w.refusal
}

func TestGvisorLayoutOnlySelectsPrepareOrOpenWithoutIssuingAuthority(t *testing.T) {
	for _, kind := range []string{"absent", "directory", "file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := privateTempDir(t)
			runtime := filepath.Join(dir, "gvisor")
			var err error
			switch kind {
			case "directory":
				err = os.Mkdir(runtime, 0700)
			case "file":
				err = os.WriteFile(runtime, []byte("untrusted saved bytes"), 0600)
			case "symlink":
				err = os.Symlink(privateTempDir(t), runtime)
			}
			if err != nil {
				t.Fatal(err)
			}
			witness := &gvisorLayoutWitness{refusal: errors.New("no original runtime was issued")}
			owner := t.Context()
			_, err = prepareGvisorLayout(owner, witness, wall.Spec{ID: "abcdef23", SessionDir: dir})
			if kind == "absent" || kind == "directory" {
				if !errors.Is(err, witness.refusal) || witness.owner != owner {
					t.Fatal("layout replaced the original context or driver refusal", err)
				}
				if witness.prepare != boolCount(kind == "absent") || witness.open != boolCount(kind == "directory") {
					t.Fatal("a saved directory was prepared over, or new layout was opened", witness)
				}
			} else if err == nil || witness.prepare != 0 || witness.open != 0 {
				t.Fatal("a substituted runtime layout entered the driver", err)
			}
		})
	}
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}
