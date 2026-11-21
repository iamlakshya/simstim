package egress

import (
	"net"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress/classify"
)

// CheckClassifierConfigurationV1 is the pure startup preflight shared with
// the actual configured factory. It opens no file, starts no program and
// receives no caller, credential lookup or publication capability.
func CheckClassifierConfigurationV1(policy api.Policy) error {
	if !trustedJudgeSettings(policy) {
		return errJudgeTransport
	}
	if _, err := classifierSettingsFor(policy); err != nil {
		return err
	}
	values := policy.Classifier
	if values.Judge == nil || *values.Judge != "model" {
		return classify.CheckConfigured(values, "")
	}
	if values.Endpoint == nil {
		return errJudgeTransport
	}
	endpoint, checked, protocol, err := judgeEndpoint(*values.Endpoint)
	if err != nil {
		return err
	}
	key := ""
	if values.Key != nil {
		key = *values.Key
	}
	if key != "" && (endpoint.Scheme != "https" || checked.Port != 443 || !validRegistryName(key)) {
		return errJudgeTransport
	}
	if key == "" && endpoint.Scheme == "http" {
		ip := net.ParseIP(endpoint.Hostname())
		if endpoint.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return errJudgeTransport
		}
	}
	return classify.CheckConfigured(values, protocol)
}
