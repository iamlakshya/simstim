package host

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func nativeAccountRowFixtureV1() Manifest {
	generation := strings.Repeat("a", 32)
	return Manifest{ID: "abc234de", Wall: &WallRecord{Level: "microvm", Driver: "chv"}, Life: &Life{Phase: LifeRunning, Started: time.Unix(1771234567, 0)},
		Run: Runtime{Generation: generation, BootID: BootID(), StartedAt: time.Unix(1771234567, 0),
			NativeAccountMode: NativeAccountOwnV1, NativeAccountGeneration: generation}}
}

func TestNativeAccountRowRequiresCurrentActiveProtectedRunData(t *testing.T) {
	complete := nativeAccountRowFixtureV1()
	if !nativeAccountActiveV1(&complete, true) {
		t.Fatal("current descriptive run projection was lost")
	}
	for _, change := range []func(*Manifest){
		func(m *Manifest) {
			m.Run.NativeAccountMode = ""
			m.Run.Label = NativeAccountOwnV1
			m.Guest = &GuestClaims{Reason: NativeAccountOwnV1}
		},
		func(m *Manifest) { m.Run.NativeAccountMode = "future" },
		func(m *Manifest) { m.Run.NativeAccountGeneration = strings.Repeat("b", 32) },
		func(m *Manifest) { m.Run.Generation = "" },
		func(m *Manifest) { m.Run.BootID = "previous-native-boot" },
		func(m *Manifest) { m.Run.StartedAt = time.Time{} },
		func(m *Manifest) { m.Life.Phase = LifeStopped },
		func(m *Manifest) { m.Life.Phase = LifeEnded },
		func(m *Manifest) { m.Life.Phase = LifeBooting },
		func(m *Manifest) { m.Wall.Driver = "gvisor" },
		func(m *Manifest) { m.Wall = nil },
		func(m *Manifest) { m.SandboxOf = "../unowned" },
	} {
		fixture := CloneManifest(complete)
		change(&fixture)
		if nativeAccountActiveV1(&fixture, true) {
			t.Fatal("old, stopped, unknown, guest or unrelated data issued an active own-account row")
		}
	}
	if nativeAccountActiveV1(&complete, false) {
		t.Fatal("a dead holder retained an active own-account row")
	}
}

func TestNativeAccountRuntimeFieldsRemainTypedAndCannotBeShadowed(t *testing.T) {
	for _, raw := range []string{
		`{"native_account_mode":"own-account","native_account_mode":"own-account"}`,
		`{"native_account_generation":"a","Native_Account_Generation":"a"}`,
	} {
		if err := json.Unmarshal([]byte(raw), new(Runtime)); err == nil {
			t.Fatal("an ambiguous known account field reached the row projection")
		}
	}
	value := Runtime{extra: map[string]json.RawMessage{"native_account_mode": json.RawMessage(`"own-account"`)}}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Runtime
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.NativeAccountMode != "" {
		t.Fatal("opaque runtime metadata overrode an omitted typed account field")
	}
}
