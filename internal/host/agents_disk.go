package host

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/image"
)

func hostAgentsDiskOptions() image.AgentsDiskOptions {
	return image.AgentsDiskOptions{
		AllowExt4Fallback: true,
	}
}

// EnsureAgentsDisk returns the host's newest read-only agents disk, reusing
// the stored disk when the installed agents have not changed and rebuilding it
// when an agent was installed, updated, replaced or removed (spec 20.4).
func EnsureAgentsDisk(ctx context.Context) (rec api.AgentsDiskRecord, rebuilt bool, err error) {
	sources := InstalledAgentSources(ctx)
	rec, rebuilt, err = ImageStore().EnsureAgentsDisk(ctx, sources, hostAgentsDiskOptions())
	return rec, rebuilt, imageProtoError(err)
}

// AttachAgentsDisk prepares the host's read-only agents disk for a microvm
// session start, records its SHA-256 digest on the trusted manifest (m.Agents),
// and returns the host path to pass read-only to the VMM (spec 20.4).
// Running sandboxes keep their recorded digest until their next start.
func AttachAgentsDisk(ctx context.Context, m *Manifest) (string, error) {
	rec, _, err := EnsureAgentsDisk(ctx)
	if err != nil {
		return "", err
	}
	if m != nil {
		m.Agents = rec.Digest
	}
	return rec.Path, nil
}

// AgentsDiskPath resolves a pinned agents disk digest (from Manifest.Agents)
// inside the host's image store without rebuilding.
func AgentsDiskPath(digest string) (string, error) {
	p, err := ImageStore().ResolveAgentsDisk(digest)
	return p, imageProtoError(err)
}

// AgentsDiskCheck reports the status of the host's read-only agents disk for
// host doctor when one has been built on this host.
func AgentsDiskCheck() []Check {
	store := ImageStore()
	rec, err := store.LatestAgentsDisk()
	if err != nil {
		return nil
	}
	if _, serr := os.Stat(rec.Path); serr != nil {
		return []Check{{
			Name:   "agents disk",
			OK:     false,
			Detail: fmt.Sprintf("latest record %s is missing its image file", shortAgentsDigest(rec.Digest)),
			Fix:    "Run agents install or rebuild the agents disk on this host.",
		}}
	}
	format := rec.Format
	if format == "" {
		format = image.AgentsFormatEROFS
	}
	agentsList := "no agents"
	if len(rec.Agents) > 0 {
		agentsList = strings.Join(rec.Agents, ", ")
	}
	return []Check{{
		Name:   "agents disk",
		OK:     true,
		Detail: fmt.Sprintf("%s (%s, %s)", shortAgentsDigest(rec.Digest), format, agentsList),
	}}
}

func shortAgentsDigest(d string) string {
	h := strings.TrimPrefix(strings.TrimSpace(d), "sha256:")
	if len(h) > 12 {
		return "sha256:" + h[:12]
	}
	return d
}
