package configdata

import (
	"errors"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/iamlakshya/simstim/internal/api"
)

func TestCanonicalConfigToolsGuardKeepsNativeGrammarAndAliasPriority(t *testing.T) {
	for _, body := range []string{
		"[network]\ntools=[]\n",
		"[[network.rules]]\ntarget='api.openai.com'\nallow=true\nTools=['mcp']\n",
	} {
		var native api.LayerValues
		if _, err := toml.Decode(body, &native); err != nil {
			t.Fatal("raw native control changed", err)
		}
		var actual api.LayerValues
		if _, err := Decode(body, &actual); !errors.Is(err, ErrConfigTools) {
			t.Fatal("file tools silently became plain authority", err)
		}
	}
	var layer api.Layer
	if _, err := Decode("[[rules]]\ntarget='api.openai.com'\nallow=true\ntools=['mcp']\n", &layer); !errors.Is(err, ErrConfigTools) {
		t.Fatal("raw file rule tools escaped guard", err)
	}
	var values api.LayerValues
	_, err := Decode("[network]\ntools=[]\nUnknown='ask'\nunknown='deny'\n", &values)
	var alias *AliasError
	if !errors.As(err, &alias) {
		t.Fatal("tools changed existing alias priority", err)
	}
	var free struct {
		Labels map[string]string `toml:"labels"`
	}
	if _, err := Decode("[labels]\ntools='ordinary-label'\n", &free); err != nil {
		t.Fatal("unrelated free key was forbidden", err)
	}
}
