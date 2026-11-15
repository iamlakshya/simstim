package guest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func decoyFixture(t *testing.T) *proto.DecoyFilesV1 {
	t.Helper()
	definitions, err := DecoyDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	result := &proto.DecoyFilesV1{Version: 1}
	for _, definition := range definitions {
		var values []api.Secret
		for _, format := range definition.Values {
			values = append(values, api.Secret(format.Prefix+strings.Repeat(format.Alphabet[:1], format.Length-len(format.Prefix))))
		}
		content, err := RenderDecoy(definition.Name, values)
		if err != nil {
			t.Fatal(err)
		}
		result.Files = append(result.Files, proto.DecoyFileV1{Name: definition.Name, Content: string(content)})
	}
	return result
}

func decoyRoot(t *testing.T) (*os.Root, string) {
	t.Helper()
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root, directory
}

func TestDecoyTableOwnershipAndInertDefaultForms(t *testing.T) {
	definitions, err := DecoyDefinitions()
	if err != nil || len(definitions) != 2 {
		t.Fatal("missing canonical decoy rows", err)
	}
	for _, definition := range definitions {
		if definition.Name == "aws" && (!strings.HasPrefix(definition.Template, "[prod-admin]\n") || strings.Contains(definition.Template, "[default]")) {
			t.Fatal("AWS decoy became a default profile")
		}
		if definition.Name == "git" && (definition.Path != ".git-credentials" || strings.Contains(definition.Template, "credential.helper")) {
			t.Fatal("Git decoy enabled a credential helper")
		}
	}
	definitions[0].Name, definitions[0].Hosts[0], definitions[0].Values[0].Alphabet = "changed", "changed", "changed"
	again, err := DecoyDefinitions()
	if err != nil || again[0].Name == "changed" || again[0].Hosts[0] == "changed" || again[0].Values[0].Alphabet == "changed" {
		t.Fatal("a caller mutated the embedded decoy data", err)
	}
}

func TestDecoyHelperWritesOnlyCompleteCanonicalFilesAt0600(t *testing.T) {
	root, _ := decoyRoot(t)
	files := decoyFixture(t)
	if err := writeDecoys(t.Context(), root, files); err != nil {
		t.Fatal(err)
	}
	definitions, err := DecoyDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range definitions {
		data, err := root.ReadFile(definition.Path)
		if err != nil || !validateDecoyContent(definition, string(data)) {
			t.Fatalf("canonical decoy file differs: %s %v", definition.Name, err)
		}
		info, err := root.Lstat(definition.Path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			t.Fatalf("decoy mode/type: %s %v %v", definition.Name, info, err)
		}
	}
	if _, err := root.Lstat(".gitconfig"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("decoy installation configured Git credential storage")
	}
	entries, err := os.ReadDir(root.Name())
	if err != nil || len(entries) != 2 {
		t.Fatalf("decoy installation left extra transient names: %v %v", entries, err)
	}
}

func TestDecoyMalformedDisabledAndCancelledGenerationsWriteNothing(t *testing.T) {
	for _, mode := range []string{"disabled", "cancelled", "future", "partial", "unknown", "duplicate", "content"} {
		t.Run(mode, func(t *testing.T) {
			root, directory := decoyRoot(t)
			files := decoyFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch mode {
			case "disabled":
				files = nil
			case "cancelled":
				cancel()
			case "future":
				files.Version++
			case "partial":
				files.Files = files.Files[:1]
			case "unknown":
				files.Files[0].Name = "foreign"
			case "duplicate":
				files.Files[0] = files.Files[1]
			case "content":
				files.Files[0].Content = strings.Replace(files.Files[0].Content, "prod-admin", "default", 1)
			}
			err := writeDecoys(ctx, root, files)
			if mode == "disabled" && err != nil || mode != "disabled" && err == nil {
				t.Fatalf("generation admission %s = %v", mode, err)
			}
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 0 {
				t.Fatalf("rejected generation wrote files: %v %v", entries, err)
			}
		})
	}
}

func TestDecoyPreflightPreservesForeignFileBeforeAnyWrite(t *testing.T) {
	root, _ := decoyRoot(t)
	if err := root.WriteFile(".git-credentials", []byte("foreign existing file"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeDecoys(t.Context(), root, decoyFixture(t)); err == nil {
		t.Fatal("decoys overwrote an existing credential file")
	}
	if _, err := root.Lstat(".aws"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("decoys changed another destination before refusing a foreign file")
	}
	data, err := root.ReadFile(".git-credentials")
	if err != nil || string(data) != "foreign existing file" {
		t.Fatal("decoy cleanup changed a foreign file", err)
	}
}

func TestDecoyPreflightRefusesParentLinks(t *testing.T) {
	root, directory := decoyRoot(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(directory, ".aws")); err != nil {
		t.Skipf("native link fixture unavailable: %v", err)
	}
	if err := writeDecoys(t.Context(), root, decoyFixture(t)); err == nil {
		t.Fatal("decoy writer followed a home link")
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatal("decoy writer changed a link's target", err)
	}
	if _, err := root.Lstat(".git-credentials"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a link refusal still wrote another decoy")
	}
}

func TestDecoyWireRejectsUnreviewedFieldsAndTrailingData(t *testing.T) {
	raw, err := json.Marshal(decoyFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeDecoyFiles(raw); err != nil {
		t.Fatal(err)
	}
	for _, malformed := range []string{
		string(raw) + "{}",
		strings.Replace(string(raw), `"name":"aws"`, `"name":"aws","path":"/outside"`, 1),
		strings.Replace(string(raw), `"name":"aws"`, `"name":"aws","Name":"git"`, 1),
		strings.Replace(string(raw), `"v":1`, `"v":1,"mode":511`, 1),
	} {
		if _, err := decodeDecoyFiles([]byte(malformed)); err == nil {
			t.Fatal("unreviewed decoy wire fields were accepted")
		}
	}
}
