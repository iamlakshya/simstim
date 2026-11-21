package slotlaunch

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func validStart() Request {
	return Request{Op: "start", Session: "abc234de", Driver: "chv", VMM: strings.Repeat("a", 64), Kernel: strings.Repeat("b", 64), Tools: strings.Repeat("c", 64), Image: strings.Repeat("d", 64), Agents: strings.Repeat("e", 64), ToolsRootHash: strings.Repeat("f", 64), ImageRootHash: strings.Repeat("1", 64), ToolsDataBlocks: 1024, ImageDataBlocks: 2048, CPUs: 2, MemoryMiB: 4096, DiskMaxGiB: 20}
}

func TestParserRejectsUnknownAliasedDuplicateAndCrossOperationFields(t *testing.T) {
	for _, raw := range []string{`{"op":"slots","Op":"slots"}`, `{"op":"slots","op":"slots"}`, `{"op":"slots","\u006fp":"slots"}`, `{"op":"slots","session":"abc234de"}`, `{"op":"kill","session":"../x"}`, `{"op":"kill","session":null}`, `{"op":"prune","keep":null}`, `{"op":"prune","keep":["ABC"]}`, `{"op":"store","sha256":"` + strings.Repeat("a", 64) + `","size":1.1}`, `{"op":"unknown"}`, `{"op":"slots"} {"op":"slots"}`, `[]`} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	start := validStart()
	raw, err := json.Marshal(start)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(raw); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse([]byte(strings.Repeat(" ", RequestMax) + `{"op":"slots"}`)); err == nil {
		t.Fatal("accepted an oversized request")
	}
}

func TestRequestReaderPreservesExactBinaryPayload(t *testing.T) {
	raw := `{"op":"store","sha256":"` + strings.Repeat("a", 64) + `","size":4}` + "\nDATA"
	request, remaining, err := ReadRequest(strings.NewReader(raw))
	if err != nil || request.Size != 4 {
		t.Fatalf("%+v: %v", request, err)
	}
	payload, err := io.ReadAll(remaining)
	if err != nil || string(payload) != "DATA" {
		t.Fatalf("payload %q: %v", payload, err)
	}
	for _, raw := range []string{`{"op":"slots"}`, strings.Repeat(" ", RequestMax) + "\n"} {
		if _, _, err := ReadRequest(strings.NewReader(raw)); err == nil {
			t.Fatal("accepted an incomplete or oversized line")
		}
	}
}

func TestCopyOnlyNamesExactCheckpointLayout(t *testing.T) {
	for _, name := range []string{"abc234de", "abc234de/checkpoints/20261119T112233.456Z"} {
		if _, _, ok := diskName(name); !ok {
			t.Errorf("refused %q", name)
		}
	}
	for _, name := range []string{"abc234de/../other", "abc234de/checkpoints/20261119T112233Z", "abc234de/checkpoints/20261119T112233.456Z/disk", "abc234de/checkpoints/20261119T112233.456Z.disk"} {
		if _, _, ok := diskName(name); ok {
			t.Errorf("accepted %q", name)
		}
	}
}

func FuzzParse(f *testing.F) {
	for _, seed := range []string{`{"op":"slots"}`, `{"op":"kill","session":"abc234de"}`, `{"op":"prune","keep":[]}`, `{"op":"slots","op":"start"}`, `{"op":"copy","from":"abc234de","to":"abc234df"}`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		r, err := Parse(raw)
		if err == nil {
			if len(raw) > RequestMax || r.validate() != nil {
				t.Fatal("parser accepted invalid request")
			}
			if _, ok := fields[r.Op]; !ok {
				t.Fatal("parser accepted unknown operation")
			}
		}
	})
}
