//go:build (linux || darwin) && (amd64 || arm64)

package image

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func agentsConvertedFixture(t *testing.T) (*os.File, [32]byte) {
	t.Helper()
	folder, root := agentTestFolder(t)
	if err := os.WriteFile(filepath.Join(folder, "agent"), []byte("test-only installed executable\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var input bytes.Buffer
	if _, err := WriteAgentsTarV1(context.Background(), []AgentsInputV1{{HostPath: "/host/agents", Folder: root}}, []AgentBinaryV1{{Name: "agent", ResolvedPath: "/host/agents/agent"}}, &input); err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(t.TempDir(), "disk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	if err := ConvertAgentsV1(context.Background(), bytes.NewReader(input.Bytes()), file); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectAgentsV1(file); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	return file, sha256.Sum256(data)
}

func TestAgentsExt4HasNoVerityTrailerAndCompleteOutputCap(t *testing.T) {
	file, _ := agentsConvertedFixture(t)
	size, err := InspectAgentsV1(file)
	if err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	if err != nil || size != info.Size() || size > MaxAgentsDisk {
		t.Fatal("ext4 contains a trailer or exceeds4GiB", size, err)
	}
	if _, err := file.WriteAt([]byte("extra"), size); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectAgentsV1(file); err == nil {
		t.Fatal("non-ext4 trailer accepted as an agents disk")
	}
}

func TestAgentsOutputFDRejectsOverlapOverflowBeforeWriting(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "bounded")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	view := &virtualFile{file: file, limit: MaxAgentsDisk, failure: agentsDiskError}
	if _, err := view.Seek(MaxAgentsDisk, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := view.Write([]byte{1}); err == nil {
		t.Fatal("one byte beyond complete4GiBdisk accepted")
	}
	if _, err := view.Seek(1, io.SeekCurrent); err == nil {
		t.Fatal("overflowseek accepted")
	}
	info, err := file.Stat()
	if err != nil || info.Size() != 0 {
		t.Fatal("refused output changed physical bytes", err)
	}
}

func TestAgentsConverterRefusesWrongOwnerTraversalDuplicateAndSpecialHeader(t *testing.T) {
	for _, header := range []string{
		"wrong-owner", "absolute", "traversal", "duplicate", "device",
	} {
		var input bytes.Buffer
		if err := writeBadAgentsTar(&input, header); err != nil {
			t.Fatal(err)
		}
		if err := normaliseAgentsTar(context.Background(), &input, io.Discard); err == nil {
			t.Fatal("unsafeagentsconverterheaderaccepted", header)
		}
	}
}
