package onboard

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
)

func putCreationHistory(t *testing.T, raw []byte) string {
	t.Helper()
	path := filepath.Join(client.StateDir(), "hosts", client.HostFileKey("box"), "removal.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{path: raw, path + ".lock": nil} {
		if err := os.WriteFile(name, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestCreateResolvesOnlyCompletedLocalHistoryBeforeActualProviderCall(t *testing.T) {
	for _, version := range []int{0, 1} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			p := newFake(t, "fake-creation-journal")
			raw := []byte(fmt.Sprintf(`{"version":%d,"host":"box","provider":"earlier-provider","machine":"earlier-id","created":"earlier-stamp","done":["destroy"]}`, version))
			path := putCreationHistory(t, raw)
			lock, err := os.Stat(path + ".lock")
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			p.onCreate = func() {
				calls++
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("actual provider call preceded committed journal clearing: %v", err)
				}
				if now, err := os.Stat(path + ".lock"); err != nil || !os.SameFile(lock, now) {
					t.Fatal("pre-provider clearing replaced its original sentinel", err)
				}
			}
			h, err := Create(t.Context(), p, providerRequest("box"))
			if err != nil || calls != 1 {
				t.Fatalf("same actual creation attempt = %d calls, %v", calls, err)
			}
			if h.Machine == nil || h.Machine.Provider != p.Name() || h.Machine.ID != provider.MachineName("box") || h.Machine.Created == "" || h.Machine.Created == "earlier-stamp" {
				t.Fatal("old local history supplied the new provider response or creation stamp")
			}
		})
	}
}

func TestCreateNeverCallsProviderForProtectedJournal(t *testing.T) {
	for _, raw := range []string{
		`{"host":"box","done":[]}`,
		`{"version":null,"host":"box","done":["destroy"]}`,
		`{"host":"Box","done":["destroy"]}`,
		`{"host":"box","done":["destroy"],"future":true}`,
		`{"host":"box","done":["destroy"],"purge":{}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			p := newFake(t, "fake-protected-journal")
			path := putCreationHistory(t, []byte(raw))
			calls := 0
			p.onCreate = func() { calls++ }
			if _, err := Create(t.Context(), p, providerRequest("box")); err == nil || calls != 0 {
				t.Fatalf("protected journal reached actual provider call: calls=%d, %v", calls, err)
			}
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, []byte(raw)) {
				t.Fatalf("protected journal bytes changed: %v", err)
			}
			if _, err := client.FindHost("box"); err == nil {
				t.Fatal("a refused preflight saved a new host")
			}
		})
	}
}
