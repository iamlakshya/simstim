package guest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestOrdinaryGuestCannotClaimVMPrepareReadiness(t *testing.T) {
	var buffer bytes.Buffer
	guest := &Init{}
	guest.request(proto.NewWriter(&buffer), proto.Request{ID: 17, Op: proto.CtlVMPrepare, Args: json.RawMessage(`{"session":"abcdefgh","vm":{"version":1,"system":"reset"}}`)})
	frame, err := proto.Read(bufio.NewReader(&buffer))
	if err != nil {
		t.Fatal(err)
	}
	var response proto.Response
	if err := json.Unmarshal(frame.Payload, &response); err != nil {
		t.Fatal(err)
	}
	if response.OK || response.Error == nil || response.ID != 17 || len(response.Result) != 0 {
		t.Fatal("ordinary guest fabricated applied v2")
	}
}

func TestHolderManifestContainsSecretNamesButNoValues(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "run", "member")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(directory, "holder.json")
	args := proto.CtlStartArgs{Session: "abcdefgh", Agent: "shell", Argv: []string{"/bin/sh"}, Cwd: "/work", Env: []string{"HOME=/home/agent", "PATH=/usr/bin:/bin"}, Secrets: map[string]string{"EXPOSED_KEY": "private-value-never-in-holder-file"}}
	if err := writeManifest(&Init{root: root}, args, destination); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("private-value-never-in-holder-file")) || !bytes.Contains(data, []byte("EXPOSED_KEY")) {
		t.Fatal("holder manifest lost names or persisted secret values")
	}
}
