package inputtransfer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func inputFileWire(t *testing.T, file api.InputFile, data []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	kind := "file"
	if file.Directory {
		kind = "directory"
	}
	if err := sendItem(t.Context(), proto.NewWriter(&out), item{Kind: kind, Size: file.Size, SHA256: file.SHA256},
		func(target io.Writer) error { _, err := target.Write(data); return err }); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func fileDeclaration(name string, data []byte) api.InputFile {
	sum := sha256.Sum256(data)
	return api.InputFile{Path: name, Size: int64(len(data)), Mode: 0600, SHA256: hex.EncodeToString(sum[:])}
}

func TestDestinationParentRetargetCannotWriteProtectedSibling(t *testing.T) {
	options, directory := receiverFixture(t)
	skills := filepath.Join(directory, "home", "agent", "skills")
	kept := filepath.Join(directory, "home", "agent", "kept")
	if err := os.Mkdir(skills, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(kept, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(kept, "input")
	if err := os.WriteFile(marker, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	identity, err := os.Stat(skills)
	if err != nil {
		t.Fatal(err)
	}
	options.Protected = []string{"/home/agent/kept"}
	retargeted := false
	options.BeforeCreate = func(ctx context.Context, parent *os.Root) error {
		current, err := parent.Stat(".")
		if err != nil {
			return err
		}
		if !retargeted && os.SameFile(identity, current) {
			if err = os.Rename(skills, skills+"-old"); err != nil {
				return err
			}
			if err = os.Symlink("kept", skills); err != nil {
				t.Skip(err)
			}
			retargeted = true
		}
		return ctx.Err()
	}
	data := []byte("new input")
	file := fileDeclaration("/home/agent/skills/input", data)
	err = receiveFile(t.Context(), bufio.NewReader(bytes.NewReader(inputFileWire(t, file, data))), options, 0, file)
	if err == nil || !retargeted {
		t.Fatalf("retarget was accepted: %v", err)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "untouched" {
		t.Fatal("protected sibling changed")
	}
	entries, err := os.ReadDir(skills + "-old")
	if err != nil || len(entries) != 0 {
		t.Fatalf("owned temporary file survived normal failure: %v", err)
	}
}

func TestDestinationContainedParentAliasAndLeafReplacement(t *testing.T) {
	options, directory := receiverFixture(t)
	home := filepath.Join(directory, "home", "agent")
	if err := os.Mkdir(filepath.Join(home, "actual"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("actual", filepath.Join(home, "alias")); err != nil {
		t.Skip(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("do not overwrite"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, "actual", "input")); err != nil {
		t.Fatal(err)
	}
	data := []byte("copied")
	file := fileDeclaration("/home/agent/alias/input", data)
	if err := receiveFile(t.Context(), bufio.NewReader(bytes.NewReader(inputFileWire(t, file, data))), options, 0, file); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "do not overwrite" {
		t.Fatal("leaf alias referent was followed")
	}
	info, err := os.Lstat(filepath.Join(home, "actual", "input"))
	if err != nil || !info.Mode().IsRegular() {
		t.Fatal("leaf alias was not replaced by the regular copied file")
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(home, "escape")); err != nil {
		t.Fatal(err)
	}
	file.Path = "/home/agent/escape/outside"
	if err := receiveFile(t.Context(), bufio.NewReader(bytes.NewReader(inputFileWire(t, file, data))), options, 0, file); err == nil {
		t.Fatal("parent alias escaped the chosen guest filesystem root")
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "do not overwrite" {
		t.Fatal("escaping parent changed the outside marker")
	}
}

func TestInterruptedFileKeepsOldTargetAndRemovesImplicitAncestors(t *testing.T) {
	options, directory := receiverFixture(t)
	target := filepath.Join(directory, "home", "agent", "input")
	if err := os.WriteFile(target, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	file := fileDeclaration("/home/agent/input", []byte("new data"))
	wire := inputFileWire(t, file, []byte("new data"))
	wire = wire[:len(wire)-1]
	if err := receiveFile(t.Context(), bufio.NewReader(bytes.NewReader(wire)), options, 0, file); err == nil {
		t.Fatal("interrupted input completed")
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "old" {
		t.Fatal("partial copy replaced the old target")
	}
	file.Path = "/home/agent/new/one/two/input"
	if err := receiveFile(t.Context(), bufio.NewReader(bytes.NewReader(wire)), options, 0, file); err == nil {
		t.Fatal("interrupted nested input completed")
	}
	if _, err := os.Stat(filepath.Join(directory, "home", "agent", "new")); !os.IsNotExist(err) {
		t.Fatal("owned empty ancestors survived ordinary interrupted-copy cleanup")
	}
}

func TestDirectoryRoleIsNotEmptyFileAndRefusesLeafAlias(t *testing.T) {
	options, directory := receiverFixture(t)
	home := filepath.Join(directory, "home", "agent")
	file := api.InputFile{Path: "/home/agent/empty", Mode: 0700, SHA256: api.EmptyInputHash, Directory: true}
	if err := receiveFile(t.Context(), bufio.NewReader(bytes.NewReader(inputFileWire(t, file, nil))), options, 0, file); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(home, "empty")); err != nil || !info.IsDir() {
		t.Fatal("explicit directory became a fake empty file")
	}
	// An existing actual directory is allowed, even with user contents.
	if err := os.WriteFile(filepath.Join(home, "empty", "existing"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := receiveFile(t.Context(), bufio.NewReader(bytes.NewReader(inputFileWire(t, file, nil))), options, 0, file); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("empty", filepath.Join(home, "alias")); err != nil {
		t.Skip(err)
	}
	file.Path = "/home/agent/alias"
	if err := receiveFile(t.Context(), bufio.NewReader(bytes.NewReader(inputFileWire(t, file, nil))), options, 0, file); err == nil {
		t.Fatal("directory leaf alias accepted")
	}
}
