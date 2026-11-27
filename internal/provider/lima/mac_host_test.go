package lima

import (
	"crypto/ed25519"
	"github.com/iamlakshya/simstim/internal/supply"
	"go.yaml.in/yaml/v3"
	"golang.org/x/crypto/ssh"
	"strings"
	"testing"
)

func TestJoinedMacProfilePreservesClosedPlainVM(t *testing.T) {
	item := supply.NewItem(supply.KindVMImage, "lima-ubuntu", "26.04-20261016", "arm64", []byte("image"), supply.ReleaseFolder("1.0.0")+"/base.img")
	key, keyErr := ssh.NewPublicKey(ed25519.PublicKey(make([]byte, 32)))
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	profile, err := MacHostYAMLV1(MacHostProfileV1{Account: "dev", CPUs: 2, MemoryBytes: 2 << 30, SSHPort: 60022, Nested: true, ImagePath: "/usr/local/share/guest/base.img", Image: item, PublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := yaml.Unmarshal(profile, &fields); err != nil {
		t.Fatal("invalid native Lima YAML", err)
	}
	for _, part := range []string{"vmType: vz", "plain: true", "hostLoopback: false", "localPort: 60022", "loadDotSSHPubKeys: false", "nestedVirtualization: true", "mounts: []", "portForwards: []", "system: false", "user: false", "grep -Fqx", " >> /home/dev/.ssh/authorized_keys"} {
		if !strings.Contains(string(profile), part) {
			t.Fatalf("missing %q", part)
		}
	}
	if strings.Contains(string(profile), "1000:1000") {
		t.Fatal("guest account ownership was guessed from a different Linux account")
	}
}
