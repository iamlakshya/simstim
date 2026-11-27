package clienttest

import "github.com/iamlakshya/simstim/internal/client"

// HostDetails is one fixture of a host's details, with a line of each
// kind. The test of the launcher's host details and the test of `host
// show` both use it, so they check the same data
// (docs/contributing/cli-spec.md 11.6).
func HostDetails() client.HostDetails {
	return client.HostDetails{
		Host: client.Host{
			Name: "box", SSH: "dev@203.0.113.7", SSHConfig: "~/src/infra/ssh/config",
			Labels:  map[string]string{"provider": "gcp", "region": "eu"},
			Machine: &client.Machine{Provider: "lima", ID: "box-1a2b", IdleStop: "2h"},
			Secrets: []string{"ANTHROPIC_API_KEY"},
		},
		Tier: client.TierOwn,
		Info: &client.HostInfo{
			Hostname: "box", OS: "linux", Arch: "amd64", Distro: "Debian 13", CPUs: 4, MemTotal: 8 << 30, DiskFree: 61 << 30,
			Version: "0.1.2", User: "dev", MemoryMax: 6551502848,
			Agents:        map[string]string{"claude": "/usr/local/bin/claude", "codex": "/usr/local/bin/codex", "shell": "/bin/bash"},
			AgentVersions: map[string]string{"claude": "2.1.3", "codex": "0.46.0"},
		},
		Sessions: 2,
	}
}
