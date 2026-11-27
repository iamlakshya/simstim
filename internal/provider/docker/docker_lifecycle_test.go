package docker_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
	"github.com/iamlakshya/simstim/internal/provider/docker"
)

func TestSavedLifecycleKeepsOriginalEngineConfigAndResource(t *testing.T) {
	dir := setup(t)
	host, err := fixtureCreate(t, t.Context(), provider.Request{Name: "box"})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := (docker.Provider{}).Open(host)
	if err != nil {
		t.Fatal(err)
	}
	original := host.Machine.Source.Endpoint
	config := host.Machine.Source.Config.Path
	host.Machine.Source.Endpoint = "unix://" + filepath.Join(t.TempDir(), "foreign.sock")
	host.Machine.ID = "foreign"
	t.Setenv("DOCKER_HOST", host.Machine.Source.Endpoint)
	t.Setenv("DOCKER_CONTEXT", "foreign")
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	if err := os.WriteFile(filepath.Join(dir, "log"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if status, err := lifecycle.Status(t.Context()); err != nil || status != client.MachineRunning {
		t.Fatalf("original status = %s %v", status, err)
	}
	transport, err := lifecycle.Transport(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := transport.Run(t.Context(), "printf original", nil)
	if err != nil || string(data) != "original" {
		t.Fatalf("original transport = %q %v", data, err)
	}
	if hint := transport.ReachHint(); !strings.Contains(hint, original) || !strings.Contains(hint, config) || !strings.Contains(hint, fixtureResource) {
		t.Fatalf("reach hint lost the original locator: %s", hint)
	}
	if err := lifecycle.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Destroy(t.Context()); err != nil {
		t.Fatal(err)
	}
	prefix := "--host " + original + " --config " + config + " "
	for _, line := range strings.Split(strings.TrimSpace(calls(dir)), "\n") {
		if !strings.HasPrefix(line, prefix) {
			t.Fatalf("saved lifecycle used an ambient selector: %s", line)
		}
	}
	for _, effect := range []string{"exec -i -u user " + fixtureResource, "stop -t 5 " + fixtureResource, "start " + fixtureResource, "rm -f " + fixtureResource} {
		if !strings.Contains(calls(dir), effect) {
			t.Fatalf("original resource effect missing %q: %s", effect, calls(dir))
		}
	}
}

func TestSavedLifecycleRefusesChangedEngineBeforeContainerEffect(t *testing.T) {
	dir := setup(t)
	host, err := fixtureCreate(t, t.Context(), provider.Request{Name: "box"})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := (docker.Provider{}).Open(host)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "engine-id"), []byte("\"foreign-engine\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "log"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Destroy(t.Context()); err == nil {
		t.Fatal("changed engine accepted an original resource effect")
	}
	if log := calls(dir); strings.Contains(log, "rm -f") || strings.Contains(log, "container inspect") {
		t.Fatalf("changed engine used a container resource: %s", log)
	}
}

func TestSavedLifecycleRefusesReplacedConfigurationBeforeAnyCommand(t *testing.T) {
	dir := setup(t)
	host, err := fixtureCreate(t, t.Context(), provider.Request{Name: "box"})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := (docker.Provider{}).Open(host)
	if err != nil {
		t.Fatal(err)
	}
	config := host.Machine.Source.Config.Path
	if err := os.Rename(config, config+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(config, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "log"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Stop(t.Context()); err == nil {
		t.Fatal("replaced configuration accepted a container effect")
	}
	if log := calls(dir); log != "" {
		t.Fatalf("replaced configuration reached the CLI: %s", log)
	}
}

func TestSavedLifecycleMissingOriginalNeverDeletesReusedName(t *testing.T) {
	dir := setup(t)
	host, err := fixtureCreate(t, t.Context(), provider.Request{Name: "box"})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := (docker.Provider{}).Open(host)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "resource-replaced"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "log"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Destroy(t.Context()); err != nil {
		t.Fatal(err)
	}
	if status, err := lifecycle.Status(t.Context()); err != nil || status != client.MachineMissing {
		t.Fatalf("missing original = %s %v", status, err)
	}
	if log := calls(dir); strings.Contains(log, "rm -f") {
		t.Fatalf("missing original selected a reusable name: %s", log)
	}
	if _, err := os.Stat(filepath.Join(dir, "state-"+provider.MachineName("box"))); err != nil {
		t.Fatalf("reused name was deleted: %v", err)
	}
}
