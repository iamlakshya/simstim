package configdata

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

type mapShapeFixture struct {
	Values map[string]string `toml:"values"`
	UI     struct {
		Motion string `toml:"motion"`
	} `toml:"ui"`
}

func TestKnownMapShapeRefusesPinnedSilentDiscardBeforePublication(t *testing.T) {
	for _, text := range []string{
		"values=1\n", "values=true\n", "values='command-value-marker'\n",
		"values=[]\n", "values=[1,2]\n",
		"values=[{x='a'},{x='b'}]\n", "[[values]]\nx='a'\n",
	} {
		t.Run(text, func(t *testing.T) {
			var native mapShapeFixture
			if _, err := toml.Decode(text, &native); err != nil || native.Values != nil {
				t.Fatalf("pinned native baseline changed: %v, %+v", err, native.Values)
			}
			var destination mapShapeFixture
			destination.Values = map[string]string{"kept": "original"}
			destination.UI.Motion = "original"
			_, _, err := DecodeTable(text+"[ui]\nmotion='off'\n", &destination)
			if !errors.Is(err, ErrConfigMapShape) || strings.Contains(err.Error(), "command-value-marker") {
				t.Fatalf("known wrong map was discarded or echoed: %v", err)
			}
			if !reflect.DeepEqual(destination.Values, map[string]string{"kept": "original"}) || destination.UI.Motion != "original" {
				t.Fatal("the preflight published partial destination state")
			}
		})
	}
}

func TestKnownMapShapeKeepsAbsenceExactNamesAndNativeValueChecks(t *testing.T) {
	var absent mapShapeFixture
	if _, err := Decode("", &absent); err != nil || absent.Values != nil {
		t.Fatalf("an absent map changed: %+v, %v", absent, err)
	}
	var exact mapShapeFixture
	if _, err := Decode("[values]\nToken='one'\nTOKEN='two'\n", &exact); err != nil || len(exact.Values) != 2 || exact.Values["Token"] != "one" || exact.Values["TOKEN"] != "two" {
		t.Fatalf("free map names were folded: %+v, %v", exact, err)
	}
	var empty mapShapeFixture
	if _, err := Decode("values={}\n", &empty); err != nil || empty.Values == nil || len(empty.Values) != 0 {
		t.Fatalf("an explicit empty table changed: %+v, %v", empty, err)
	}
	var wrongElement mapShapeFixture
	if _, err := Decode("[values]\nx=1\n", &wrongElement); err == nil || errors.Is(err, ErrConfigMapShape) {
		t.Fatalf("native scalar map value check changed: %v", err)
	}
	var unknown mapShapeFixture
	if _, err := Decode("unknown=[{values=1}]\n", &unknown); err != nil {
		t.Fatalf("an unknown subtree acquired a schema: %v", err)
	}
}

func TestKnownMapShapeDescendsOnlyKnownContainersAndKeepsAliasPriority(t *testing.T) {
	for _, text := range []string{"Values=1\n[values]\nx='a'\n", "values=1\n[Values]\nx='a'\n"} {
		var destination mapShapeFixture
		_, err := Decode(text, &destination)
		var alias *AliasError
		if !errors.As(err, &alias) || errors.Is(err, ErrConfigMapShape) {
			t.Fatalf("aliases lost their existing precedence: %v", err)
		}
	}
	type nested struct {
		Names map[string]map[string]string `toml:"names"`
		Rows  []map[string]string          `toml:"rows"`
	}
	for _, text := range []string{"[names]\nchild=1\n", "rows=[1]\n"} {
		var destination nested
		if _, err := Decode(text, &destination); !errors.Is(err, ErrConfigMapShape) {
			t.Fatalf("a nested known map vanished: %v", err)
		}
	}
}

type customMapShape map[string]string

func (m *customMapShape) UnmarshalText(text []byte) error {
	*m = customMapShape{"received": string(text)}
	return nil
}

func TestKnownMapShapeLeavesNativeHooksAndFinalPinnedValuesAlone(t *testing.T) {
	var custom struct {
		Value customMapShape `toml:"value"`
	}
	if _, err := Decode("value='text'\n", &custom); err != nil || custom.Value["received"] != "text" {
		t.Fatalf("native custom map decoder was rejected: %+v, %v", custom, err)
	}
	// The pinned parser's final returned table wins over transient mapping
	// states. This is valid final UI data, not permission to reject raw events.
	var final mapShapeFixture
	if _, err := Decode(`ui={motion.extra=1,motion="auto",""=[]}`, &final); err != nil || final.UI.Motion != "auto" {
		t.Fatalf("the canonical final value changed: %+v, %v", final, err)
	}
	if err := CheckMapShape(reflect.TypeFor[string](), false); err != nil {
		t.Fatal("a non-map type acquired an unrelated shape requirement")
	}
}
