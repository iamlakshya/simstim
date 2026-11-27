package helpers

import "testing"

func TestNewSignedBundleRolesRemainClosed(t *testing.T) {
	for _, name := range []string{"launch-amd64", "launch-arm64", "image-verifier-darwin-amd64", "image-verifier-darwin-arm64", "image-verifier-linux-amd64", "image-verifier-linux-arm64"} {
		if !isHelper(name) {
			t.Fatal("the same signed bundle discarded its exact new executable role", name)
		}
	}
	for _, name := range []string{"launch-future", "launch-amd64.json", "image-verifier-linux-386", "image-verifier-windows-amd64", "image-verifier-linux-amd64.extra"} {
		if isHelper(name) {
			t.Fatal("a future or unrelated file entered a signed executable role", name)
		}
	}
}
