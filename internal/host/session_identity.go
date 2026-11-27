package host

import (
	"sort"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

// applySessionIdentity runs after user environment layers and effective new
// naming. Legacy absent labels/names stay explicitly unknown; physical host
// names, remote rows and later rename aliases never supply identity.
func applySessionIdentity(m *Manifest) {
	values := make(map[string]string, len(m.Env)+4)
	for _, entry := range m.Env {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	hostLabel, sandbox := m.HostLabel, m.SandboxName
	if hostLabel == "" {
		hostLabel = "unknown"
	}
	if sandbox == "" {
		sandbox = "unknown"
	}
	values = api.ApplySessionIdentity(values, api.SessionIdentity{Session: m.ID, Host: hostLabel, Agent: m.Agent.Kind, Sandbox: sandbox})
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	m.Env = make([]string, 0, len(keys))
	for _, key := range keys {
		m.Env = append(m.Env, key+"="+values[key])
	}
}
