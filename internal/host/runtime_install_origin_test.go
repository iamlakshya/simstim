package host_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/supply"
)

// This isolated filesystem fixture invokes the actual closed installer in a
// child containing TestMain's real install-payload dispatch. It exercises no
// SSH, provider, VM, root step, session or fabricated native boot admission.
func TestNativeInstallerRecordsOriginalFilesAndRemovesItsStage(t *testing.T) {
	if os.Getuid() <= 0 || runtime.GOOS == "windows" {
		t.Skip("the isolated native installer fixture requires a normal user on a supported host filesystem")
	}
	h := hosttest.New(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	nonce := strings.Repeat("a", 32)
	stage := filepath.Join(h.Home, "."+brand.Name+"-install-"+nonce)
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(stage, brand.HostBinary)
	if err := os.WriteFile(program, body, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	plan := api.InstallPlanV1{Version: 1, Nonce: nonce, HelperVersion: "origin-test", Arch: runtime.GOARCH, Items: []api.InstallItemV1{{Kind: supply.KindHelper, Name: brand.HostBinary, Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}}}
	header, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	input := append([]byte(fmt.Sprintf("%08x\n", len(header))), header...)
	input = append(input, body...)
	command := exec.CommandContext(t.Context(), program, "install-payload")
	command.Stdin = bytes.NewReader(input)
	output, err := command.CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte("__INSTALLED__")) {
		t.Fatal("native installation did not complete", err, string(output))
	}
	installed, err := os.ReadFile(filepath.Join(paths.BinDir(), plan.HelperVersion, brand.HostBinary))
	if err != nil || !bytes.Equal(installed, body) {
		t.Fatal("installed bytes differ from the original checked stream", err)
	}
	if _, err := os.Lstat(stage); !os.IsNotExist(err) {
		t.Fatal("successful installer retained its own stage", err)
	}
	entries, err := os.ReadDir(h.Home)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "."+brand.Name+"-purge-") {
			raw, err := os.ReadFile(filepath.Join(h.Home, entry.Name(), "installs.json"))
			if err != nil {
				t.Fatal(err)
			}
			var record map[string]any
			if json.Unmarshal(raw, &record) != nil || record["scope"] == "" || record["host_id"] == "" || record["versions"] == nil {
				t.Fatal("original installation has no native owner archive")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("native installer created no outside ownership guard")
	}
}
