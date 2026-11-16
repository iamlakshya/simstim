package helpers

import (
	"bytes"
	"testing"
)

func TestNamespaceLauncherParticipatesInHelperVersion(t *testing.T) {
	resetFetched(t)
	fetchMu.Lock()
	fetched = map[string][]byte{"host-linux-amd64": []byte("HOST"), "userns-linux-amd64": []byte("STATIC-LAUNCHER")}
	fetchMu.Unlock()
	data, ok := Userns("linux", "amd64")
	if !ok || !bytes.Equal(data, []byte("STATIC-LAUNCHER")) {
		t.Fatalf("namespace launcher = %q, %v", data, ok)
	}
	before := Version()
	fetchMu.Lock()
	fetched["userns-linux-amd64"] = []byte("UPDATED-STATIC-LAUNCHER")
	fetchMu.Unlock()
	if Version() == before {
		t.Fatal("changing the namespace launcher kept the helper version")
	}
	if !isHelper("userns-linux-arm64") {
		t.Fatal("a signed bundle's namespace launcher is ignored")
	}
}
