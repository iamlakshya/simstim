package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var gvisorReleaseRolesV1 = []string{"runsc", "gvisor_sentry", "gvisor-sentry-prewarmer", "gvisor-proc-umounter", "runsc-fd-parking"}

type gvisorSourcePinsV1 struct{ upstream, patch, version, released string }

// Read the actual shared native source pin; do not copy a version constant
// into the release writer or import the runtime/host initialisers here.
func readGvisorSourcePinsV1(repository string) (gvisorSourcePinsV1, error) {
	var pins gvisorSourcePinsV1
	patch, err := readGvisorRegularV1(filepath.Join(repository, "third_party/gvisor/hostguard.patch"), 16<<20)
	if err != nil {
		return pins, err
	}
	digest := sha256.Sum256(patch)
	pins.patch = hex.EncodeToString(digest[:])
	data, err := readGvisorRegularV1(filepath.Join(repository, "internal/wall/gvisor/native_build.go"), 64<<10)
	if err != nil {
		return pins, err
	}
	file, err := parser.ParseFile(token.NewFileSet(), "native_build.go", data, parser.SkipObjectResolution)
	if err != nil || file.Name.Name != "gvisor" {
		return pins, fmt.Errorf("the native gvisor source pin is malformed")
	}
	values := map[string]string{}
	for _, declaration := range file.Decls {
		group, ok := declaration.(*ast.GenDecl)
		if !ok || group.Tok != token.CONST {
			continue
		}
		for _, declaration := range group.Specs {
			value, ok := declaration.(*ast.ValueSpec)
			if !ok || len(value.Names) != 1 {
				continue
			}
			name := value.Names[0].Name
			if name != "NativePatchSHA256V1" && name != "NativeBuildVersionV1" {
				continue
			}
			if _, duplicate := values[name]; duplicate || len(value.Values) != 1 {
				return pins, fmt.Errorf("the native gvisor source pin is ambiguous")
			}
			literal, ok := value.Values[0].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return pins, fmt.Errorf("the native gvisor pin must be an exact string literal")
			}
			values[name], err = strconv.Unquote(literal.Value)
			if err != nil {
				return pins, err
			}
		}
	}
	if values["NativePatchSHA256V1"] != pins.patch {
		return pins, fmt.Errorf("the gvisor patch differs from its native source pin")
	}
	pins.version = values["NativeBuildVersionV1"]
	parts := strings.Split(pins.version, "-")
	if len(parts) != 3 || parts[0] != "release" || parts[2] != pins.patch[:8] {
		return pins, fmt.Errorf("the gvisor build version is not derived from the actual source patch")
	}
	day, revision, ok := strings.Cut(parts[1], ".")
	date, err := time.Parse("20060102", day)
	_, revisionErr := strconv.ParseUint(revision, 10, 64)
	if !ok || err != nil || revisionErr != nil {
		return pins, fmt.Errorf("the gvisor source pin has no original release date")
	}
	pins.released = date.Format("2006-01-02")
	input, err := readGvisorRegularV1(filepath.Join(repository, "third_party/gvisor/hostguard-inputs.json"), 4<<20)
	if err != nil {
		return pins, err
	}
	var inputs struct {
		Upstream string          `json:"upstream_commit"`
		Files    json.RawMessage `json:"files"`
	}
	if err := json.Unmarshal(input, &inputs); err != nil {
		return pins, err
	}
	commit, err := hex.DecodeString(inputs.Upstream)
	if err != nil || len(commit) != 20 || inputs.Upstream != strings.ToLower(inputs.Upstream) || len(inputs.Files) == 0 {
		return pins, fmt.Errorf("the gvisor source inputs have no exact upstream commit and file closure")
	}
	pins.upstream = inputs.Upstream
	return pins, nil
}

// Hash and inspect one captured regular descriptor. A name replacement or a
// symlink cannot supply different bytes after the native file check.
func readGvisorRegularV1(path string, limit int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() <= 0 || before.Size() > limit {
		return nil, fmt.Errorf("gvisor input is not a bounded regular file: %s", filepath.Base(path))
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() {
		return nil, fmt.Errorf("the gvisor input changed during capture")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != after.Size() || int64(len(data)) > limit {
		return nil, fmt.Errorf("the gvisor input changed size during capture")
	}
	return data, nil
}
