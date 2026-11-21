//go:build linux

package host_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
)

func TestNoneActualCreatorPublishesDirectoryDataAndStoppedRmUsesANewOperation(t *testing.T) {
	requireManagerlessNoneFixtureV1(t)
	hosttest.New(t)
	s := start(t, "none-storage", "/bin/sh", "-c", "printf NONE-STORAGE; exec sleep 60")
	waitOutput(t, s.ID, "NONE-STORAGE")
	m, err := host.LoadManifest(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Version    int    `json:"v"`
		Primary    string `json:"primary"`
		Boot       string `json:"boot"`
		Generation string `json:"generation"`
	}
	if len(m.NoneDirectoryCreation) == 0 || json.Unmarshal(m.NoneDirectoryCreation, &data) != nil || data.Version != 1 || data.Primary != m.ID || data.Boot != m.Run.BootID || data.Generation != m.Run.Generation {
		t.Fatal("the actual creator did not publish its exact protected directory DATA")
	}
	stopNoneInheritedFixtureV1(t, s.ID)
	if _, err := host.Delete(s.ID); err != nil {
		t.Fatal("a new actual Delete could not remove the clean stopped None", err)
	}
	if _, err := os.Lstat(paths.SessionDir(s.ID)); !os.IsNotExist(err) {
		t.Fatal("the stopped None metadata was not removed", err)
	}
}
