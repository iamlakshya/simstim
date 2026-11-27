package lima

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
)

// host create pins the new VM's host key, through Lima's own SSH config,
// before it hands the host over (isolation spec 14.7.1).
func TestCreatePinsTheHostKey(t *testing.T) {
	dir := fakeLima(t, "linux")
	var out bytes.Buffer
	h, err := fixtureCreate(t, context.Background(), provider.Request{Name: "dev", Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	inst := provider.MachineName("dev")
	got, err := os.ReadFile(filepath.Join(dir, "pinned"))
	if err != nil || string(got) != "dev lima-"+inst+" "+h.SSHConfig {
		t.Errorf("pinned %q, %v", got, err)
	}
	if want := "  ✓ host key SHA256:" + strings.Repeat("A", 43) + " pinned on first use\n"; !strings.Contains(out.String(), want) {
		t.Errorf("output lacks %q:\n%s", want, out.String())
	}
}

// A failed pin retains the actual VM and records its original locator; it
// never deletes another instance through a reusable ambient name.
func TestCreateRetainsAVMWithoutAPin(t *testing.T) {
	dir := fakeLima(t, "linux")
	set(t, dir, "pin-fails")
	_, err := fixtureCreate(t, context.Background(), provider.Request{Name: "dev"})
	var ue *client.UserError
	inst := provider.MachineName("dev")
	if !errors.As(retainedCreationCause(err), &ue) || !strings.HasPrefix(ue.Msg, "could not pin the host key of the new VM "+inst) ||
		!strings.HasSuffix(ue.Msg, "; the VM is retained for inspection") || ue.Hint != retainedHintPrefix+"run this again" {
		t.Fatalf("create = %#v", err)
	}
	if cs := calls(t, dir); !slices.Equal(verbs(cs), []string{"create", "start"}) {
		t.Errorf("limactl calls: %q", cs)
	}
}
