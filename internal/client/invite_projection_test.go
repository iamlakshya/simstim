package client

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestActualJoinedProjectionExcludesAmbientIdentitiesAndExec(t *testing.T) {
	host := savedJoinedFixture(t)
	ambient := filepath.Join(home(), ".ssh", "config")
	if err := os.MkdirAll(filepath.Dir(ambient), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "Host *\n IdentityFile /operator-only-key\nMatch exec /bin/false\n Include /late-unowned-include\n"
	if err := os.WriteFile(ambient, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	transport := &SSH{Host: host}
	owned, finish, err := transport.beginNativeSource(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	path, err := writeWrapperContext(owned, host)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(generated)
	if strings.Contains(text, ambient) || strings.Contains(text, "operator-only-key") || strings.Contains(text, "late-unowned-include") || strings.Count(text, "Include ") != 1 || !strings.Contains(text, host.SSHConfig) {
		t.Fatalf("joined projection reread ambient configuration: %s", text)
	}
	changed := cloneGrantHost(host)
	changed.SSH += "-wrong"
	if _, err = writeWrapperContext(owned, changed); err == nil {
		t.Fatal("a native lease for another route requested closed mode")
	}
}
func TestJoinedOwnershipDataDoesNotRequestClosedWrapper(t *testing.T) {
	host := Host{Name: "box", SSH: "joined-box", SSHConfig: "/caller-config", JoinedSSH: &JoinedSSHOwnership{Version: 1}}
	allowed, err := joinedProjectionWrapperAllowedV1(context.Background(), host)
	if err != nil || allowed {
		t.Fatal("ownership data alone became a native projection")
	}
	if _, _, err = (&SSH{Host: host}).beginNativeSource(context.Background()); err == nil {
		t.Fatal("a fabricated joined Host acquired an actual native Source")
	}
}
