package client

import (
	"os"
	"runtime"
	"testing"
)

func TestSetupTrustIsKeptByHostAndRepository(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if got := SetupTrust("box"); len(got) != 0 {
		t.Fatalf("fresh: %v", got)
	}
	if err := RememberSetup("box", "/home/me/src/app", true); err != nil {
		t.Fatal(err)
	}
	if err := RememberSetup("box", "/home/me/src/api", false); err != nil {
		t.Fatal(err)
	}
	if err := RememberSetup("other", "/home/me/src/app", false); err != nil {
		t.Fatal(err)
	}
	got := SetupTrust("box")
	if run, ok := got["/home/me/src/app"]; !ok || !run {
		t.Errorf("app: %v", got)
	}
	if run, ok := got["/home/me/src/api"]; !ok || run {
		t.Errorf("api: %v", got)
	}
	if run := SetupTrust("other")["/home/me/src/app"]; run {
		t.Error("an answer leaked to another host")
	}
	st, err := os.Stat(trustPath())
	// Windows has no Unix file modes to check.
	if err != nil || (runtime.GOOS != "windows" && st.Mode().Perm() != 0o600) {
		t.Errorf("the answers file: %v %v", st, err)
	}
}
