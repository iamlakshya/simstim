package cli

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

// Every address that the JSON gives finds its own session again: the ID
// stands in for a name that another session has as its name or its ID.
func TestAddressesResolveBack(t *testing.T) {
	list := []client.Session{
		{ID: "abcdefgh", Name: "web"},
		{ID: "zzzzzzzz", Name: "abcdefgh"}, // its name is the other's ID
		{ID: "c3c3c3c3", Name: "dup"},
		{ID: "d4d4d4d4", Name: "dup"},
		{ID: "e5e5e5e5", Name: ""},
		{ID: "f6f6f6f6", Name: "f6f6f6f6"}, // its name is its own ID
	}
	for _, s := range list {
		addr, _ := sessionObject("box", s)["address"].(string)
		ref := strings.TrimPrefix(addr, "box/")
		got, err := findIn(nil, list, ref, "box")
		if err != nil || got.ID != s.ID {
			t.Errorf("%s/%q: the address %s finds %+v, %v", s.ID, s.Name, addr, got, err)
		}
	}
}
