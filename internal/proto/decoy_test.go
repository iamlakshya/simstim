package proto

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestDecoyPrivateWireCloneAndFormattingKeepContentsPrivate(t *testing.T) {
	input := &DecoyFilesV1{Version: 1, Files: []DecoyFileV1{{Name: "git", Content: "private-generated-file"}}}
	output := CloneDecoyFilesV1(input)
	output.Files[0].Content = "changed"
	if input.Files[0].Content != "private-generated-file" {
		t.Fatal("private decoy clone borrowed its file list")
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		if strings.Contains(fmt.Sprintf(format, input), "private-generated-file") || strings.Contains(fmt.Sprintf(format, input.Files[0]), "private-generated-file") {
			t.Fatal("decoy formatting exposed generated file contents")
		}
	}
	wire, err := json.Marshal(input)
	if err != nil || !strings.Contains(string(wire), "private-generated-file") {
		t.Fatal("the private wire did not carry the exact generated contents", err)
	}
}

func TestDecoyImageExclusionsComeFromTheSameEmbeddedRows(t *testing.T) {
	definitions, err := DecoyDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	paths, err := DecoyPaths()
	if err != nil || len(paths) != len(definitions) {
		t.Fatal("decoy capture exclusions lost a row", err)
	}
	for index := range definitions {
		if definitions[index].Path != paths[index] {
			t.Fatal("decoy image exclusions used a different path table")
		}
	}
	paths[0] = "caller mutation"
	again, err := DecoyPaths()
	if err != nil || again[0] == "caller mutation" {
		t.Fatal("an image caller changed the canonical exclusion list", err)
	}
}
