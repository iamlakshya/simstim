package client

import (
	"os"
	"strings"
	"testing"
)

// The same bounded reader serves runtime commands and config check. Its
// error describes the file limit without claiming it belongs to one command.
func TestConfigSizeErrorFitsEveryReader(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(ConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hostsPath(), []byte(strings.Repeat("#", MaxConfigSize+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadConfigFile(hostsPath()); err == nil || err.Error() != "it is larger than 1 MiB" {
		t.Fatalf("shared reader error = %v", err)
	}
	if hosts, err := LoadHosts(); len(hosts) != 0 || err == nil || !strings.HasSuffix(err.Error(), "it is larger than 1 MiB") {
		t.Fatalf("runtime reader = %+v, %v", hosts, err)
	}
	if check := fileCheck("hosts.toml", hostsPath(), &hostsFile{}); check.OK || check.Detail != "it is larger than 1 MiB" || check.Fix == "" {
		t.Fatalf("doctor check = %+v", check)
	}
}
