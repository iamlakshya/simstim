package egress

import (
	"context"
	"net"
	"net/http"
	"reflect"
	"slices"
	"strconv"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/secretbind"
)

// applyTrustedJudgeHeadersLocked is the sole private trusted-role injector.
// The actual fixed-payload model caller holds policyMu. Guest request
// planning cannot invoke it or manufacture its private Source context.
func (s *Server) applyTrustedJudgeHeadersLocked(ctx context.Context, request *http.Request, capability *judgeModelCapability) error {
	if capability == nil || capability.server != s || request == nil || request.URL == nil || request.Method != http.MethodPost || request.URL.String() != capability.endpoint.String() || !capability.currentSettings() {
		return errJudgeTransport
	}
	source, current := s.judgeSourceSnapshotLocked(ctx)
	if !current || ctx.Err() != nil {
		return errJudgeTransport
	}
	if capability.key == "" {
		return nil
	}
	secret, exists := source.secrets[capability.key]
	if !exists || !secret.present || !secret.trustedArmed || secret.value == "" || secret.trusted == nil || capability.settings.Model == nil || capability.settings.Endpoint == nil {
		return errJudgeTransport
	}
	use := secret.trusted
	if use.Version != 1 || use.Use != api.TrustedJudgeUseV1 || use.Mode != api.SecretModeBound || use.Model != *capability.settings.Model || use.Endpoint != *capability.settings.Endpoint || capability.endpoint.Scheme != "https" || capability.request.Port != 443 {
		return errJudgeTransport
	}
	endpoint, checked, _, err := judgeEndpoint(use.Endpoint)
	if err != nil || endpoint.String() != capability.endpoint.String() || !reflect.DeepEqual(checked, capability.request) {
		return errJudgeTransport
	}
	binding, err := secretbind.Canonical(use.Bind)
	if err != nil || !reflect.DeepEqual(binding, use.Bind) || !slices.Contains(binding.Hosts, net.JoinHostPort(capability.request.Host, strconv.Itoa(capability.request.Port))) {
		return errJudgeTransport
	}
	kind, user, err := secretbind.ParseFormat(binding.Format)
	value, valid := requestCredentialValue(kind, user, secret.value)
	if err != nil || !valid || len(value)+len(binding.Header)+4 > maxHTTPHeaderBytes {
		return errJudgeTransport
	}
	request.Header.Set(binding.Header, string(value))
	return nil
}
