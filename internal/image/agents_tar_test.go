//go:build (linux || darwin) && (amd64 || arm64)

package image

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func agentTestFolder(t *testing.T) (string, *os.Root) {
	t.Helper()
	name := t.TempDir()
	root, err := os.OpenRoot(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return name, root
}

func TestAgentsTarSameInstallsAreDeterministicWithExactOwnerModeAndBinaryLink(t *testing.T) {
	folder, root := agentTestFolder(t)
	if err := os.Mkdir(filepath.Join(folder, "versions"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(folder, "versions", "agent")
	if err := os.WriteFile(file, []byte("installed executable bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0o755); err != nil {
		t.Fatal(err)
	}
	fixed := time.Unix(1700000000, 123000000)
	if err := os.Chtimes(file, fixed, fixed); err != nil {
		t.Fatal(err)
	}
	inputs := []AgentsInputV1{{HostPath: "/host/install", Folder: root}}
	binary := []AgentBinaryV1{{Name: "agent", ResolvedPath: "/host/install/versions/agent"}}
	var first, second bytes.Buffer
	a, err := WriteAgentsTarV1(context.Background(), inputs, binary, &first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := WriteAgentsTarV1(context.Background(), inputs, binary, &second)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := AgentsSourceDigestV1(context.Background(), inputs, binary)
	if err != nil {
		t.Fatal(err)
	}
	if a != b || a != metadata || !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("unchanged installs changed tar/source digest")
	}
	reader := tar.NewReader(bytes.NewReader(first.Bytes()))
	var names []string
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Uid != 1000 || header.Gid != 1000 {
			t.Fatal("host ownership entered guest disk")
		}
		names = append(names, header.Name)
		if header.Name == "bin/agent" && (header.Typeflag != tar.TypeSymlink || header.Linkname != "../root/host/install/versions/agent") {
			t.Fatal("resolved PATH binary link was lost")
		}
		if header.Name == "root/host/install/versions/agent" && (header.Mode != 0o755 || !header.ModTime.Equal(fixed)) {
			t.Fatal("installed metadata was clipped")
		}
	}
	expected := []string{"bin", "bin/agent", "root", "root/host", "root/host/install", "root/host/install/versions", "root/host/install/versions/agent"}
	if !reflect.DeepEqual(names, expected) {
		t.Fatal("tar order/parent ordering changed", names)
	}
}

func TestAgentsTarDoesNotCopyExternalSymlinkTarget(t *testing.T) {
	folder, root := agentTestFolder(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "private"), []byte("OUTSIDE_BODY_MUST_NOT_COPY"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(folder, "jump")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := WriteAgentsTarV1(context.Background(), []AgentsInputV1{{HostPath: "/host/install", Folder: root}}, nil, &out); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out.Bytes(), []byte("OUTSIDE_BODY_MUST_NOT_COPY")) {
		t.Fatal("walker read a target outside its root")
	}
	reader := tar.NewReader(bytes.NewReader(out.Bytes()))
	found := false
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Name == "root/host/install/jump" {
			found = true
			if header.Typeflag != tar.TypeSymlink || header.Linkname != outside {
				t.Fatal("symlink metadata changed")
			}
		}
	}
	if !found {
		t.Fatal("symlink was silently dropped")
	}
}

func TestAgentsSourceReplacementWithSameSizeAndMtimeChangesDigest(t *testing.T) {
	folder, root := agentTestFolder(t)
	name := filepath.Join(folder, "agent")
	fixed := time.Unix(1700000000, 0)
	if err := os.WriteFile(name, []byte("first"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(name, fixed, fixed); err != nil {
		t.Fatal(err)
	}
	inputs := []AgentsInputV1{{HostPath: "/host/install", Folder: root}}
	first, err := AgentsSourceDigestV1(context.Background(), inputs, nil)
	if err != nil {
		t.Fatal(err)
	}
	temporary := filepath.Join(folder, "replacement")
	if err := os.WriteFile(temporary, []byte("other"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(temporary, fixed, fixed); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporary, name); err != nil {
		t.Fatal(err)
	}
	second, err := AgentsSourceDigestV1(context.Background(), inputs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("same-size/time replacement reused stale disk")
	}
}

func TestAgentsTarPreservesCallerFileOffset(t *testing.T) {
	name := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(name, []byte("0123456789"), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Seek(3, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteAgentsTarV1(context.Background(), []AgentsInputV1{{HostPath: "/host/agent", File: file}}, []AgentBinaryV1{{Name: "agent", ResolvedPath: "/host/agent"}}, io.Discard); err != nil {
		t.Fatal(err)
	}
	offset, err := file.Seek(0, io.SeekCurrent)
	if err != nil || offset != 3 {
		t.Fatal("snapshot mutated caller open-file description", offset, err)
	}
}

func TestAgentsTarWholeNameOrderAcrossPrefixSiblingsAndRoots(t *testing.T) {
	folder, root := agentTestFolder(t)
	if err := os.Mkdir(filepath.Join(folder, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"a/child", "a-", "a.", "z"} {
		if err := os.WriteFile(filepath.Join(folder, relative), []byte(relative), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	other, otherRoot := agentTestFolder(t)
	if err := os.WriteFile(filepath.Join(other, "body"), []byte("other root"), 0o644); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if _, err := WriteAgentsTarV1(context.Background(), []AgentsInputV1{{HostPath: "/host/install", Folder: root}, {HostPath: "/host/install-", Folder: otherRoot}}, nil, &output); err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(bytes.NewReader(output.Bytes()))
	previous := ""
	found := map[string]bool{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Name <= previous {
			t.Fatalf("whole-name byte order regressed: %q after %q", header.Name, previous)
		}
		if parent := filepath.ToSlash(filepath.Dir(header.Name)); parent != "." && !found[parent] {
			t.Fatal("child preceded parent", header.Name)
		}
		found[header.Name] = true
		previous = header.Name
	}
	for _, expected := range []string{"root/host/install/a/child", "root/host/install/a-", "root/host/install/a.", "root/host/install-/body"} {
		if !found[expected] {
			t.Fatal("prefix-adjacent source disappeared", expected)
		}
	}
	if err := normaliseAgentsTar(context.Background(), bytes.NewReader(output.Bytes()), io.Discard); err != nil {
		t.Fatal("producer order was refused by the canonical converter", err)
	}
}
