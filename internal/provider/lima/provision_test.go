package lima

import (
	"context"
	"encoding/json"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/provider"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

func TestProvisionScriptReusesH7AndValidShell(t *testing.T) {
	script := ProvisionScript()
	if !strings.HasPrefix(script, process.UbuntuProvisionScript()) {
		t.Fatalf("ProvisionScript must start with process.UbuntuProvisionScript():\n%s", script)
	}
	if strings.Contains(script, "LIMA_CIDATA") {
		t.Errorf("ProvisionScript must not reference LIMA_CIDATA variables (Lima warns):\n%s", script)
	}
	for _, want := range []string{
		"DEBIAN_FRONTEND=noninteractive",
		"bubblewrap",
		"apparmor-profiles",
		"apparmor-utils",
		"/usr/share/apparmor/extra-profiles/bwrap-userns-restrict",
		"/etc/apparmor.d/bwrap-userns-restrict",
		"apparmor_parser -r",
		"usermod -aG kvm",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("ProvisionScript missing %q:\n%s", want, script)
		}
	}

	if runtime.GOOS != "windows" {
		cmd := exec.CommandContext(context.Background(), "/bin/sh", "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("sh -n rejected ProvisionScript: %v\n%s", err, out)
		}
	}

	for _, goos := range []string{"darwin", "linux"} {
		args := CreateArgs("vm", provider.Request{}, goos)
		var setArg string
		for _, a := range args {
			if rest, ok := strings.CutPrefix(a, "--set=.provision += "); ok {
				setArg = rest
				break
			}
		}
		if setArg == "" {
			t.Fatalf("%s CreateArgs missing --set=.provision += ...: %q", goos, args)
		}
		var entries []provisionEntry
		if err := json.Unmarshal([]byte(setArg), &entries); err != nil {
			t.Fatalf("%s provision JSON invalid: %v\n%s", goos, err, setArg)
		}
		if len(entries) != 1 || entries[0].Mode != "system" || entries[0].Script != script {
			t.Fatalf("%s provision entry = %+v, want single mode:system entry matching ProvisionScript", goos, entries)
		}
	}
}
