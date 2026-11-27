package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestJSONSafeKeepsExactEscapesWithoutChangingItsInput(t *testing.T) {
	input := []byte("{\"key\u009b\":\"A\u007f\u009b\u202e\u200b\U000e0041é→Z\",\"n\":12345678901234567890,\"already\":\"\\\\u202e\"}\n")
	original := bytes.Clone(input)
	want := []byte(`{"key\u009b":"A\u007f\u009b\u202e\u200b\udb40\udc41é→Z","n":12345678901234567890,"already":"\\u202e"}` + "\n")
	got := jsonSafe(input)
	if !bytes.Equal(got, want) || !bytes.Equal(input, original) {
		t.Fatalf("escaping changed bytes or mutated input:\ngot  %q\nwant %q", got, want)
	}
	if !reflect.DeepEqual(jsonAllocationValue(t, got), jsonAllocationValue(t, input)) {
		t.Fatal("terminal escaping changed decoded keys, values or the large integer")
	}
	got[0] = '['
	if !bytes.Equal(input, original) {
		t.Fatal("escaped output shares writable bytes with the original JSON")
	}
}

func TestJSONOutputExpansionPreservesTerminalSafetyAndDecodedValues(t *testing.T) {
	controls := "\u007f\u0085\u009b\u061c\u200b\u202e\u2066\ufeff\U000e0001\U000e0041\U000e007f"
	for _, prefix := range []int{0, 31, 32, 33, 127, 4096} {
		payload := map[string]any{
			"key":            "a different key",
			"key" + controls: strings.Repeat("x", prefix) + strings.Repeat(controls, 128),
			"number":         json.Number("12345678901234567890"),
			"nested":         []any{controls, `\u202e`, "café → ok", true, nil},
		}
		file, err := os.CreateTemp(t.TempDir(), "json-output")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = file.Close() })
		func() {
			original := os.Stdout
			os.Stdout = file
			defer func() { os.Stdout = original }()
			err = printJSON(map[string]any{"payload": payload})
		}()
		if closeErr := file.Close(); err != nil || closeErr != nil {
			t.Fatalf("prefix %d: write=%v close=%v", prefix, err, closeErr)
		}
		output, err := os.ReadFile(file.Name())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasSuffix(output, []byte("\n")) || bytes.HasSuffix(output, []byte("\n\n")) {
			t.Fatalf("prefix %d: output lost its single final newline", prefix)
		}
		for _, r := range string(output) {
			if r != '\n' && r != '\t' && api.Unclean(r) {
				t.Fatalf("prefix %d: JSON wrote raw terminal rune %U", prefix, r)
			}
		}
		want := map[string]any{"v": json.Number("1"), "payload": payload}
		if got := jsonAllocationValue(t, output); !reflect.DeepEqual(got, want) {
			t.Fatalf("prefix %d: JSON output changed decoded host data", prefix)
		}
	}
}

func jsonAllocationValue(t *testing.T, data []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("JSON output has another value or trailing data: %v", err)
	}
	return value
}
