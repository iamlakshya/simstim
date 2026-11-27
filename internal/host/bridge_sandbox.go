package host

import (
	"encoding/json"
	"errors"
	"runtime"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
)

func init() {
	Features = append(Features, "sandbox")
	if runtime.GOOS == "linux" && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64") {
		Features = append(Features, api.FeatureSandboxPrepareV1, api.FeatureVMNeedV1)
	}
}

type sandboxProbeArgsV1 struct {
	ReleaseURL string           `json:"release_url"`
	Runtime    *supply.Manifest `json:"runtime,omitempty"`
}

// handleSandbox serves the read-only diagnostic probe and the versioned fixed
// test-boot preparation route. Native boot admission consumes its original
// producers independently of diagnostic probe facts.
func (b *bridge) handleSandbox(req proto.Request) bool {
	if req.Op == "sandbox.prepare" {
		b.handleSandboxPrepareV1(req)
		return true
	}
	if req.Op != "sandbox.probe" {
		return false
	}
	if err := CheckCaller(); err != nil {
		b.reply(req.ID, nil, err)
		return true
	}
	var args sandboxProbeArgsV1
	raw := req.Args
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(raw)), "{") || !api.ExactJSONFields(raw, args) || json.Unmarshal(raw, &args) != nil {
		b.reply(req.ID, nil, UserError("bad_request", "the sandbox probe arguments are not an exact object", "Update the client and run the host doctor again."))
		return true
	}
	ctx := b.ctx
	var finish func() error
	var err error
	if args.Runtime != nil {
		if args.ReleaseURL != "" {
			b.reply(req.ID, nil, UserError("bad_request", "the selected-runtime physical probe has an extra release request", "Probe the original delivered runtime with no release URL."))
			return true
		}
		ctx, finish, err = b.gvisorProbeContextV1(req, *args.Runtime)
		if err != nil {
			b.reply(req.ID, nil, err)
			return true
		}
	}
	result, err := CapabilityProbe(ctx, args.ReleaseURL)
	if finish != nil {
		err = errors.Join(err, finish())
	}
	if err != nil {
		err = UserError("sandbox_probe", "could not probe sandbox capabilities: "+err.Error(), "Check the host's permissions and run the host doctor again.")
	}
	b.reply(req.ID, result, err)
	return true
}
