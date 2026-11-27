package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func TestGoneSessionHintRejectsUnsafeSavedHost(t *testing.T) {
	a := address{host: client.Host{Name: "b;x"}, ref: "abcd2345", byID: true}
	_, err := a.find(Root(), nil)
	var got *client.UserError
	if !errors.As(err, &got) || got.Hint != brand.Name+" ls" {
		t.Fatalf("gone session hint = %v", err)
	}
}

func TestHostHintsNeverIncludeUnsafeSavedName(t *testing.T) {
	for _, name := range []string{"b;x", "box\nexit", "box\u202e", "-option"} {
		for _, hint := range []string{newHostHint(name), hostStartHint(name)} {
			if strings.Contains(hint, name) {
				t.Errorf("unsafe host %q in hint %q", name, hint)
			}
		}
	}
}
