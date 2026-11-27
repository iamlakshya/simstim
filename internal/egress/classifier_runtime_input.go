package egress

import (
	"github.com/iamlakshya/simstim/internal/egress/classify"
	"github.com/iamlakshya/simstim/internal/seedmeta"
)

// The caller owns policyMu and has selected the exact current immutable
// registry. Optional sensitive data is omitted; a sensitive required host is
// never replaced by a masked hostname and judged as though it were real.
func (s *Server) classifierInputLocked(source *memberState, request Request, originalHost, domain string, facts classify.Facts) (classify.Input, bool) {
	if source == nil || source != s.currentMembers() {
		return classify.Input{}, false
	}
	if _, redacted := publicNetworkHost(s.scrubber, request.Host, originalHost); redacted {
		return classify.Input{}, false
	}
	metadata := source.metadata.JudgeMetadata
	if metadata.Agent == "" {
		metadata.Agent = source.metadata.Agent
	}
	input := classify.Input{Version: classify.JudgeVersion, Host: request.Host, Port: request.Port, Facts: facts}
	optional := func(value string, limit int) string {
		if cleanAuditText(value, s.scrubber.snapshot(), maxScrubInputBytes) != value {
			return ""
		}
		return truncateUTF8(value, limit)
	}
	input.RegistrableDomain = optional(domain, 253)
	input.RepositoryRemote = optional(seedmeta.Remote(metadata.RepositoryRemote), 2048)
	input.Agent = optional(metadata.Agent, 64)
	input.Profile = optional(metadata.Profile, 128)
	input.Task = optional(metadata.Task, 4096)
	for _, ecosystem := range metadata.Ecosystems {
		if safe := optional(ecosystem, 64); safe != "" {
			input.Ecosystems = append(input.Ecosystems, safe)
		}
	}
	if request.Path != "" && cleanAuditPath(request.Path, s.scrubber.snapshot()) == truncateUTF8(request.Path, 256) {
		input.Method = optional(request.Method, 64)
		if input.Method != "" {
			input.Path = truncateUTF8(request.Path, 256)
		}
	}
	if _, err := classify.EncodeInput(input); err != nil {
		return classify.Input{}, false
	}
	return input, true
}
