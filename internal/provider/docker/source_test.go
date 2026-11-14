package docker

import (
	"encoding/json"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func TestSharingIdentityUsesOriginalResourceAfterLocalHostRename(t *testing.T) {
	id := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	machine := brand.Name + "-original"
	raw, err := json.Marshal(map[string]any{"id": id, "name": "/" + machine, "label": "original", "mounts": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	var sharing dockerSharing
	if err := json.Unmarshal(raw, &sharing); err != nil {
		t.Fatal(err)
	}
	h := client.Host{Name: "renamed", Machine: &client.Machine{Provider: "docker", ID: machine}}
	if !sharingMatches(sharing, h, id) {
		t.Fatal("local host rename lost the same original closed-created resource")
	}
	sharing.Label = "foreign"
	if sharingMatches(sharing, h, id) {
		t.Fatal("foreign ownership label became a matching resource")
	}
}
