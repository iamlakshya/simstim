package vmneed

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
)

// These are metadata/store fixtures, never runtime bytes, release signatures
// or original-client/native boot authority.
func multipleVMMRuntimeV1() supply.Manifest {
	full := fixtureNeed().Runtime
	full.Items = append(full.Items, firecrackerNeed("amd64").Runtime.Items[0])
	return full
}

func TestFullRuntimeCollectionSelectsExactCHVAndFCRoles(t *testing.T) {
	full := multipleVMMRuntimeV1()
	if err := runtimecache.Validate(full, "amd64"); err != nil {
		t.Fatal(err)
	}
	if ValidateRuntimeForDriver("chv", full, "amd64") == nil {
		t.Fatal("the full collection became a one-driver Need")
	}
	for _, driver := range []string{"chv", "firecracker"} {
		t.Run(driver, func(t *testing.T) {
			selected, err := SelectRuntimeForDriver(driver, full, "amd64")
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateRuntimeForDriver(driver, selected, "amd64"); err != nil {
				t.Fatal(err)
			}
			roles, _ := RolesForDriver(driver)
			if len(selected.Items) != len(roles) || selected.Release != full.Release || selected.Runtime != full.Runtime {
				t.Fatal("selected boot metadata changed the original header")
			}
			for _, role := range roles {
				original, found := full.Find(role.Kind, role.Name, "amd64")
				actual, selectedFound := selected.Find(role.Kind, role.Name, "amd64")
				if !found || !selectedFound || !reflect.DeepEqual(actual, original) {
					t.Fatal("selection renamed or changed an original signed role")
				}
			}
		})
	}
}

func TestRuntimeProjectionCannotReplaceOriginalSignedBytes(t *testing.T) {
	full := multipleVMMRuntimeV1()
	full.Advisories = []supply.Advisory{{ID: "CVE-2026-99999", Kind: supply.KindVMM, Name: "cloud-hypervisor", Before: "1.0", Severity: "low"}}
	originalBytes, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := SelectRuntimeForDriver("chv", full, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	projectionBytes, err := json.Marshal(selected)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(projectionBytes, originalBytes) {
		t.Fatal("a selected projection was presented as the complete original document")
	}
	selected.Items[0].SHA256 = "changed"
	selected.Advisories[0].URL = "changed"
	after, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, originalBytes) {
		t.Fatal("projection mutated bytes or vectors of the original authority document")
	}
	// No signature or receipt is created here. Actual delivery retains its
	// original full request; only plan comparison consumes the projection.
}

func TestRuntimeProjectionRejectsMissingDuplicateAndWrongArchRoles(t *testing.T) {
	for name, change := range map[string]func(*supply.Manifest){
		"missing selected VMM":             func(m *supply.Manifest) { m.Items = m.Items[1:] },
		"duplicate selected VMM":           func(m *supply.Manifest) { m.Items = append(m.Items, m.Items[0]) },
		"duplicate kernel":                 func(m *supply.Manifest) { m.Items = append(m.Items, m.Items[1]) },
		"wrong architecture":               func(m *supply.Manifest) { m.Items[0].Arch = "arm64" },
		"EFI cannot replace normal kernel": func(m *supply.Manifest) { m.Items[1].Name = "guest-efi" },
		"wrong header":                     func(m *supply.Manifest) { m.Release = "" },
	} {
		t.Run(name, func(t *testing.T) {
			full := multipleVMMRuntimeV1()
			change(&full)
			if _, err := SelectRuntimeForDriver("chv", full, "amd64"); err == nil {
				t.Fatal("invalid full collection donated a boot role")
			}
		})
	}
	full := multipleVMMRuntimeV1()
	if _, err := SelectRuntimeForDriver("unknown", full, "amd64"); err == nil {
		t.Fatal("unknown driver selected signed bytes")
	}
	for i := range full.Items {
		if full.Items[i].Name == "firecracker" {
			full.Items[i].Version = "1.16.1"
		}
	}
	if _, err := SelectRuntimeForDriver("firecracker", full, "amd64"); err == nil {
		t.Fatal("the selected driver borrowed a safer alternative's floor")
	}
}
