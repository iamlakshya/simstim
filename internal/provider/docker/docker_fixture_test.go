package docker_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
	"github.com/iamlakshya/simstim/internal/provider/docker"
)

const fixtureResource = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const fixtureEngine = "fixture-original-engine"

// The executable derives its owned fixture directory from its actual path.
// The strict metadata role deliberately does not inherit FAKE_DIR. No command
// reaches a Docker engine, creates an account or starts a provider resource.
const fakeDocker = `#!/bin/sh
had_fixture=${FAKE_DIR+yes}
FAKE_DIR=${0%/*}
echo "$*" >> "$FAKE_DIR/log"
while [ "$1" = --host ] || [ "$1" = --config ]; do shift; shift; done
cmd=$1; shift
name() {
  if [ "$1" = ` + fixtureResource + ` ]; then cat "$FAKE_DIR/resource-name"; else printf '%s\n' "$1"; fi
}
state() { selected=$(name "$1"); cat "$FAKE_DIR/state-$selected" 2>/dev/null; }
case $cmd in
context)
  if [ "$1" = show ]; then printf 'fixture\n'; else printf '"unix://%s/engine.sock"\n' "$FAKE_DIR"; fi ;;
info)
  if [ -f "$FAKE_DIR/engine-id" ]; then cat "$FAKE_DIR/engine-id"
  elif [ -f "$FAKE_DIR/inspector-mismatch" ] && [ "$had_fixture" != yes ]; then
    printf '"fixture-other-engine"\n'
  else printf '"` + fixtureEngine + `"\n'; fi ;;
container)
  if [ "$1" != inspect ] || [ "$2" != ` + fixtureResource + ` ] || [ -f "$FAKE_DIR/receipt-fails" ]; then exit 1; fi
  selected=$(name "$2")
  if [ ! -f "$FAKE_DIR/state-$selected" ] || [ -f "$FAKE_DIR/resource-replaced" ]; then echo 'Error: No such container' >&2; exit 1; fi
  label=$(cat "$FAKE_DIR/resource-label")
  printf '{"id":"` + fixtureResource + `","name":"/%s","label":"%s","mounts":[]}\n' "$selected" "$label" ;;
inspect)
  selected=$(name "$3")
  if [ -f "$FAKE_DIR/state-$selected" ]; then state "$selected"; else echo "${FAKE_MISSING:-Error: No such object}: $selected" >&2; exit 1; fi ;;
run)
  while [ "$1" != --name ]; do shift; done
  selected=$2; shift; shift
  while [ "$1" != --label ]; do shift; done
  label=${2#*=}
  printf '%s\n' "$selected" > "$FAKE_DIR/resource-name"
  printf '%s\n' "$label" > "$FAKE_DIR/resource-label"
  echo running > "$FAKE_DIR/state-$selected"
  printf '` + fixtureResource + `\n' ;;
exec)
  while [ "$1" != sh ]; do last=$1; shift; done
  if [ "$(state "$last")" != running ]; then echo "Error response from daemon: container $last is not running" >&2; exit 1; fi
  shift; shift
  case $1 in *useradd*) exit 0 ;; esac
  exec sh -c "$1" ;;
start|unpause) selected=$(name "$1"); echo running > "$FAKE_DIR/state-$selected" ;;
stop) selected=$(name "$3"); echo exited > "$FAKE_DIR/state-$selected" ;;
rm) selected=$(name "$2"); rm -f "$FAKE_DIR/state-$selected" ;;
*) echo "fake docker: unknown command $cmd" >&2; exit 2 ;;
esac
`

func isolateDockerHome(t *testing.T) {
	t.Helper()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR"} {
		t.Setenv(key, t.TempDir())
	}
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	t.Setenv("DOCKER_HOST", "unix://"+filepath.Join(t.TempDir(), "engine.sock"))
	t.Setenv("DOCKER_CONTEXT", "")
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
}

func setup(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake docker is a POSIX shell script")
	}
	isolateDockerHome(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(fakeDocker), 0755); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "config")
	if err := os.Mkdir(config, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DIR", dir)
	t.Setenv("DOCKER_CONFIG", config)
	t.Setenv("DOCKER_HOST", "unix://"+filepath.Join(dir, "engine.sock"))
	return dir
}

func calls(dir string) string {
	b, _ := os.ReadFile(filepath.Join(dir, "log"))
	return string(b)
}

// fixtureCreate uses the real closed source scope against owned fixture files.
// It is a local boundary test, not a native Docker runtime witness.
func fixtureCreate(t *testing.T, ctx context.Context, req provider.Request) (client.Host, error) {
	t.Helper()
	owned, release, err := client.BeginHostCreationContext(ctx, provider.CreationSourceRequest("docker", req))
	if err != nil {
		return client.Host{}, err
	}
	defer release()
	if err := client.CheckHostCreationRemovalContext(owned, req.Name); err != nil {
		return client.Host{}, err
	}
	if err := client.StartHostCreationContext(owned); err != nil {
		return client.Host{}, err
	}
	return docker.Provider{}.Create(owned, req)
}

func TestCreateRequiresOriginalClosedScopeBeforeAnyProviderCommand(t *testing.T) {
	dir := setup(t)
	if _, err := (docker.Provider{}).Create(t.Context(), provider.Request{Name: "box"}); err == nil {
		t.Fatal("unscoped creation reached the provider")
	}
	if log := calls(dir); log != "" {
		t.Fatalf("unscoped creation ran commands: %s", log)
	}
}

func TestCreateChecksSameEngineInStrictInspectorBeforeWrites(t *testing.T) {
	dir := setup(t)
	if err := os.WriteFile(filepath.Join(dir, "inspector-mismatch"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	_, err := fixtureCreate(t, t.Context(), provider.Request{Name: "box"})
	var refused *client.UserError
	if !errors.As(err, &refused) || refused.Code != "source_locator" {
		t.Fatalf("strict engine mismatch = %v", err)
	}
	if log := calls(dir); strings.Contains(log, "run -d") || strings.Contains(log, "exec -u") || strings.Contains(log, "rm -f") {
		t.Fatalf("inspector mismatch performed a container write: %s", log)
	}
}

func TestCreateRetainsOriginalResourceAfterReceiptFailure(t *testing.T) {
	dir := setup(t)
	if err := os.WriteFile(filepath.Join(dir, "receipt-fails"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	result, err := fixtureCreate(t, t.Context(), provider.Request{Name: "box"})
	var partial *provider.CreationFailure
	if !errors.As(err, &partial) || partial.ResourceID != fixtureResource || partial.Locator != "unix://"+filepath.Join(dir, "engine.sock") || partial.Host.Machine == nil || result.Machine == nil || partial.Host.Machine.ID != result.Machine.ID {
		t.Fatalf("receipt failure lost original resource: %+v %v", result, err)
	}
	if log := calls(dir); strings.Contains(log, "rm -f") {
		t.Fatalf("receipt failure deleted a reusable container name: %s", log)
	}
	if _, err := os.Stat(filepath.Join(dir, "state-"+provider.MachineName("box"))); err != nil {
		t.Fatalf("retained resource disappeared: %v", err)
	}
}
