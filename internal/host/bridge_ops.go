package host

import (
	"bytes"
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// moreFeatures are the features that set hosts and sessions up: the requests
// in this file, a new session's secrets (secrets.go), its missing folder
// (folders.go), trust in a repository's setup script (setup.go), and the
// resume of a session whose agent ended (resume.go).
var moreFeatures = []string{"purge", "facts", "drain", "agents.check", "doctor.fix", "secrets", "mkdir", "setup.trust", "resume"}

func init() { Features = append(Features, moreFeatures...) }

// handleMore serves the requests that set a host up, and the resume of a
// session. It reports whether it knew the request.
func (b *bridge) handleMore(req proto.Request) bool {
	if b.handleStopV1(req) {
		return true
	}
	if b.handleRewindPreparationV1(req) {
		return true
	}
	if b.handleForkV1(req) {
		return true
	}
	if b.handleCheckpointV1(req) {
		return true
	}
	if b.handleResumePreparationV1(req) {
		return true
	}
	switch req.Op {
	case "host.purge":
		var args api.PurgeArgs
		if !api.ExactJSONFields(req.Args, args) || json.Unmarshal(req.Args, &args) != nil || !ValidHostID(args.HostID) || !ValidHostID(args.Nonce) {
			b.reply(req.ID, nil, UserError("bad_request", "host purge needs the exact original host and removal attempt", "Retry with the current client and saved removal record."))
			return true
		}
		result, err := PurgeContext(b.ctx, args)
		b.reply(req.ID, result, err)
	case "host.purge.commit":
		var witness api.PurgeGuardWitness
		if !api.ExactJSONFields(req.Args, witness) || json.Unmarshal(req.Args, &witness) != nil || !witness.Prepared() {
			b.reply(req.ID, nil, UserError("bad_request", "host purge acknowledgement needs its saved complete witness", "Keep the removal record and retry through the same client."))
			return true
		}
		b.reply(req.ID, nil, CommitPurgeGuardContext(b.ctx, witness))
	case "host.drain":
		var args api.DrainArgs
		fields, shapeErr := api.JSONObjectFields(req.Args, api.ExactJSONFieldNames(args)...)
		on := bytes.TrimSpace(fields["on"])
		if shapeErr != nil || len(fields) != 2 || !bytes.Equal(on, []byte("true")) && !bytes.Equal(on, []byte("false")) || !bytes.HasPrefix(bytes.TrimSpace(fields["reason"]), []byte(`"`)) || !bytes.HasPrefix(bytes.TrimSpace(req.Args), []byte("{")) || !api.ExactJSONFields(req.Args, args) || json.Unmarshal(req.Args, &args) != nil {
			b.reply(req.ID, nil, UserError("bad_request", "host drain needs exact on and reason settings", "Use a matching current client to change the drain setting."))
			return true
		}
		result, err := SetDrainContext(b.ctx, args)
		b.reply(req.ID, result, err)
	case "host.facts":
		var args struct{}
		raw := req.Args
		if len(raw) == 0 {
			raw = json.RawMessage(`{}`)
		}
		if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) || !api.ExactJSONFields(raw, args) || json.Unmarshal(raw, &args) != nil {
			b.reply(req.ID, nil, UserError("bad_request", "host facts need an exact empty object", "Use a matching current client to request host facts."))
			return true
		}
		result, err := HostFacts(b.ctx)
		b.reply(req.ID, result, err)
	case "arm":
		var a api.ArmSecretsArgs
		if !api.ExactJSONFields(req.Args, (*api.ArmSecretsArgs)(nil)) || json.Unmarshal(req.Args, &a) != nil {
			b.reply(req.ID, nil, UserError("bad_request", "arm needs exact supported secret arguments", "Use a matching client and the original bound secret names."))
			return true
		}
		result, err := ArmSecrets(a)
		b.reply(req.ID, json.RawMessage(result), err)
	case "agents.check":
		b.reply(req.ID, AgentChecks(), nil)
	case "doctor.fix":
		var a api.FixArgs
		if len(req.Args) > 0 {
			if err := json.Unmarshal(req.Args, &a); err != nil {
				b.reply(req.ID, nil, UserError("bad_request", "the fix request is not valid JSON", "Update the client."))
				return true
			}
		}
		b.reply(req.ID, Fixes(a), nil)
	case "resume":
		if err := validateResumeInputShape(req.Args); err != nil {
			b.reply(req.ID, nil, err)
			return true
		}
		var a api.ResumeArgs
		if err := validateResumeSecretShape(req.Args); err != nil {
			b.reply(req.ID, nil, err)
			return true
		}
		if err := json.Unmarshal(req.Args, &a); err != nil {
			b.reply(req.ID, nil, UserError("bad_request", "the resume request is not valid JSON", "Update the client."))
			return true
		}
		if a.Prepared != nil {
			v, err := b.resumePreparedV1(req, a)
			b.reply(req.ID, v, err)
			return true
		}
		if a.NamedSSHKeys != nil {
			b.reply(req.ID, nil, resumePreparationErrorV1("named keys need their original cold preparation"))
			return true
		}
		v, err := ResumeContext(b.ctx, a)
		b.reply(req.ID, v, err)
	default:
		return false
	}
	return true
}
