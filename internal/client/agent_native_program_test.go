//go:build darwin || linux

package client

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNativeProgramRetainsOriginalExecutableInode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ssh")
	image := []byte{0x7f, 'E', 'L', 'F', 1, 2, 3, 4}
	if err := os.WriteFile(path, image, 0o700); err != nil {
		t.Fatal(err)
	}
	program, err := openNativeAgentProgram(path)
	if err != nil {
		t.Fatal(err)
	}
	defer program.Close()
	if !sameNativeAgentProgram(program, path) {
		t.Fatal("the retained regular native image changed")
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, image, 0o700); err != nil {
		t.Fatal(err)
	}
	if sameNativeAgentProgram(program, path) {
		t.Fatal("accepted a replacement executable with identical contents")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".old", path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(image, 0), 0o700); err != nil {
		t.Fatal(err)
	}
	if sameNativeAgentProgram(program, path) {
		t.Fatal("accepted an in-place size change after the pre-Start snapshot")
	}
}

func TestNativeProgramRefusesScriptsAndIncompleteImages(t *testing.T) {
	for _, image := range []string{"", "ELF", "#!/bin/sh\nexec ssh \"$@\"\n"} {
		path := filepath.Join(t.TempDir(), "ssh")
		if err := os.WriteFile(path, []byte(image), 0o700); err != nil {
			t.Fatal(err)
		}
		if program, err := openNativeAgentProgram(path); err == nil {
			_ = program.Close()
			t.Fatal("accepted an unknown or script-wrapped SSH program")
		}
	}
}
