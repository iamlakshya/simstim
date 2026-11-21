// Package limaprofile renders the fixed plain VM profile used by Mac joining.
// It has no provider, client, transport or execution authority.
package limaprofile

import (
	"encoding/json"
	"fmt"
	"golang.org/x/crypto/ssh"
	"net/url"
	"path"
	"strings"

	"github.com/iamlakshya/simstim/internal/supply"
)

// MacHostProfileV1 is fixed local join data, never a generic provider request.
type MacHostProfileV1 struct {
	Account     string
	CPUs        int
	MemoryBytes int64
	SSHPort     uint16
	Nested      bool
	ImagePath   string
	Image       supply.Item
	PublicKey   string
}

// MacHostYAMLV1 creates a closed plain profile from exact checked release
// image data. No template, user default or override chooses sharing features.
func MacHostYAMLV1(p MacHostProfileV1) ([]byte, error) {
	if !validMacAccount(p.Account) || p.CPUs <= 0 || p.MemoryBytes < 256<<20 || p.SSHPort == 0 || !path.IsAbs(p.ImagePath) || path.Clean(p.ImagePath) != p.ImagePath || strings.ContainsAny(p.ImagePath, "\x00\r\n") || p.Image.Kind != supply.KindVMImage || p.Image.Arch != "arm64" || !supply.IsHex64(p.Image.SHA256) || strings.ContainsAny(p.PublicKey, "\x00\r\n") {
		return nil, fmt.Errorf("the Mac host VM needs its fixed profile and exact pinned image")
	}
	key, _, options, rest, keyErr := ssh.ParseAuthorizedKey([]byte(p.PublicKey))
	if keyErr != nil || len(options) != 0 || len(rest) != 0 {
		return nil, fmt.Errorf("the Mac profile needs the original plain invitation public key")
	}
	if _, certificate := key.(*ssh.Certificate); certificate {
		return nil, fmt.Errorf("the Mac profile cannot use an SSH certificate as its invitation key")
	}
	quote := func(value string) string { data, _ := json.Marshal(value); return string(data) }
	imageURL := (&url.URL{Scheme: "file", Path: p.ImagePath}).String()
	text := fmt.Sprintf("vmType: vz\narch: aarch64\nplain: true\ncpus: %d\nmemory: %s\ndisk: 40GiB\nnestedVirtualization: %t\nimages:\n  - location: %s\n    arch: aarch64\n    digest: %s\nmounts: []\nportForwards: []\ncontainerd:\n  system: false\n  user: false\nnetworkOpts:\n  hostLoopback: false\nssh:\n  localPort: %d\n  loadDotSSHPubKeys: false\nuser:\n  name: %s\n  uid: 501\n  home: %s\n  shell: /bin/bash\nprovision:\n  - mode: system\n    script: |\n      #!/bin/sh\n      set -eu\n      install -d -m 0700 /home/%s/.ssh\n      touch /home/%s/.ssh/authorized_keys\n      key=%s\n      grep -Fqx -- \"$key\" /home/%s/.ssh/authorized_keys || printf '%%s\\n' \"$key\" >> /home/%s/.ssh/authorized_keys\n      chown -R %s: /home/%s/.ssh\n      chmod 0600 /home/%s/.ssh/authorized_keys\n", p.CPUs, quote(fmt.Sprintf("%dB", p.MemoryBytes)), p.Nested, quote(imageURL), quote("sha256:"+p.Image.SHA256), p.SSHPort, quote(p.Account), quote("/home/"+p.Account), p.Account, p.Account, macShellQuote(p.PublicKey), p.Account, p.Account, p.Account, p.Account, p.Account)
	return []byte(text), nil
}

func macShellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func validMacAccount(value string) bool {
	if value == "" || value == "root" || len(value) > 32 {
		return false
	}
	for i, c := range value {
		if c >= 'a' && c <= 'z' || c == '_' || i > 0 && (c >= '0' && c <= '9' || c == '-') {
			continue
		}
		return false
	}
	return true
}
