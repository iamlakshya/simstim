package lima

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/iamlakshya/simstim/internal/wall/process"
)

type provisionEntry struct {
	Mode   string `json:"mode"`
	Script string `json:"script"`
}

// ProvisionScript returns the mode: system provisioning script added to Lima
// Ubuntu LTS VMs at create time so the process wall works out of the box and
// the VM user belongs to the kvm group for nested virtualisation.
func ProvisionScript() string {
	kvmStep := strings.Join([]string{
		"if getent group kvm >/dev/null 2>&1; then",
		`  for u in $(awk -F: '$6 ~ /^\/home\// && $7 !~ /(nologin|false)$/ {print $1}' /etc/passwd); do`,
		`    usermod -aG kvm "$u"`,
		"  done",
		"fi",
	}, "\n") + "\n"
	return process.UbuntuProvisionScript() + kvmStep
}

// provisionSetArg builds the limactl --set expression that appends the system
// provisioning script to the template's provision list.
func provisionSetArg() string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode([]provisionEntry{{
		Mode:   "system",
		Script: ProvisionScript(),
	}}); err != nil {
		panic(err)
	}
	return "--set=.provision += " + strings.TrimSuffix(buf.String(), "\n")
}
