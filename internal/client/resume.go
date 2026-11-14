package client

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

// ResumeArgs asks a host to start a session's agent again, in the same
// session.
type ResumeArgs = api.ResumeArgs

// ResumeResult is what a resume did: the session's new row, and how its
// agent came back.
type ResumeResult = api.ResumeResult

// Resume starts the agent of a session again after it ended, with the
// agent's own resume command. The caller fetches the session's secrets
// (Session.Secrets) into a. A MemoryMax of 0 keeps the size the session
// asked for before, and MemoryAuto asks for the host's default.
func (c *Conn) Resume(ctx context.Context, a ResumeArgs) (*ResumeResult, error) {
	a = cloneResumeValues(a)
	ctx, request := ensureGrantRequest(ctx)
	if err := requireRemoteGrantRequest(ctx); err != nil {
		return nil, err
	}
	if err := requireGrantOperation(ctx, grantOpResume); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return nil, err
	}
	if !c.HasFeature("resume") {
		return nil, c.tooOldToResume()
	}
	if err := c.RequireSecrets(NewArgs{Secrets: a.Secrets}); err != nil {
		return nil, err
	}
	for _, member := range a.Members {
		if err := c.requireSecretFeature(len(member.Secrets) != 0); err != nil {
			return nil, err
		}
	}
	if err := checkUncheckedResumeSelection(ctx, c, nil); err != nil {
		return nil, err
	}
	group, err := c.needsResumeMembers(ctx, a.ID)
	if err != nil {
		return nil, err
	}
	if group || a.Snapshot != nil || a.Members != nil {
		request.mu.Lock()
		before := cloneResumeGrantIntent(request.resume)
		request.mu.Unlock()
		names := secretMapNames(a.Secrets)
		if before != nil {
			names = before.originalNames
		}
		intent, _, err := c.prepareResumeMemberSelection(ctx, a.ID, names, actualResumeNames(a))
		if err != nil {
			return nil, err
		}
		if a.Snapshot != nil && !sameGrantSnapshot(a.Snapshot, intent.snapshot) {
			return nil, joinIntentError()
		}
		a.Snapshot = cloneGrantSnapshot(intent.snapshot)
	}
	ctx, release, err := c.beginGrant(ctx, "resume", false)
	if err != nil {
		return nil, err
	}
	defer release()
	tier, err := c.checkResumeGrants(ctx, a)
	if err != nil {
		return nil, err
	}
	if err := recordSecretNames(ctx, c.Host, c.HostID(), tier, a.Secrets); err != nil {
		return nil, err
	}
	for _, member := range a.Members {
		if err := recordSecretNames(ctx, c.Host, c.HostID(), tier, member.Secrets); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var r ResumeResult
	if err := c.callWithSent(ctx, "resume", a, &r, release); err != nil {
		return nil, c.resumeError(err)
	}
	if err := checkSessionID(c.Host.Name, r.Session.ID); err != nil {
		return nil, err
	}
	if a.Snapshot != nil && r.Session.ID != a.ID {
		return nil, &UserError{Msg: "the helper returned another sandbox member after resume",
			Hint: "List sessions and use the original member's address.", Code: "protocol", Kind: KindRefused}
	}
	return &r, nil
}

// resumeError is the error of a resume request: a helper that does not know
// the request is too old.
func (c *Conn) resumeError(err error) error {
	var ue *UserError
	if errors.As(err, &ue) && ue.Code == "unknown_op" {
		return c.tooOldToResume()
	}
	return err
}

// tooOldToResume is the error for a host whose helper cannot resume.
func (c *Conn) tooOldToResume() error {
	return &UserError{Msg: "the helper on " + c.Host.Name + " is too old to resume a session",
		Hint: brand.Name + " host add " + c.Host.Name + " (updates the helper)", Code: "helper_old"}
}

// ResumeSecrets picks the secrets that a resume of session s on host h
// fetches. Those are the names that the session asks for and that h's own
// secrets in hosts.toml list, and the names that the user passed. The
// host's manifest names the secrets, so a host could ask for any of them.
// A name that neither lists is an error, which says how to pass it. So is
// a name that no secret could have. The error shows it quoted, since it
// comes from the host and could hold escapes for the terminal.
func ResumeSecrets(h Host, s Session, passed []string) ([]string, error) {
	return ResumeSecretsContext(context.Background(), h, s, passed)
}

// ResumeSecretsContext is ResumeSecrets with a context for policy reads
// and one command's terminal confirmations. Every context-bearing caller
// must use it rather than dropping that context at the gate.
func ResumeSecretsContext(ctx context.Context, h Host, s Session, passed []string) ([]string, error) {
	ctx, _ = ensureGrantRequest(ctx)
	if err := requireGrantOperation(ctx, grantOpResume); err != nil {
		return nil, err
	}
	names, err := ResumeSecretNames(h, s, passed)
	if err != nil {
		return nil, err
	}
	if err := bindRequestLocalSource(ctx, h); err != nil {
		return nil, err
	}
	_, hostID, err := grantHostTier(ctx, h)
	if err != nil {
		return nil, err
	}
	tier, p, err := EffectiveSessionTier(ctx, h, s.ID, hostID)
	if err != nil {
		return nil, err
	}
	if err := bindLegacyResumeIntent(ctx, h, hostID, s.ID, names, p); err != nil {
		return nil, err
	}
	settings := LoadSettings()
	if err := allowResumeSecretNames(ctx, h, hostID, tier, settings, names, p); err != nil {
		return nil, err
	}
	if p != nil && p.SSHAgent {
		if err := allowRequestGrant(ctx, h, hostID, tier, Outbound{Kind: OutboundSSHAgent}, settings); err != nil {
			return nil, err
		}
	}
	return names, nil
}

// ResumeSecretNames checks the names a host asks to fetch without reading
// policy or state. The real fetch and the RPC must still enforce the gate.
func ResumeSecretNames(h Host, s Session, passed []string) ([]string, error) {
	own := SecretNames(h, nil)
	sessName := resumeOneLine(s.Name)
	hostName := resumeOneLine(h.Name)
	addr, addrOK := validResumeAddr(h.Name, s.ID)
	var names, missing []string
	for _, n := range s.Secrets {
		switch {
		case !secretNameRE.MatchString(n):
			hint := ""
			if addrOK {
				hint = fmt.Sprintf("Delete the session with %s rm %s, then start a new one.", brand.Name, addr)
			}
			return nil, &UserError{
				Msg:  fmt.Sprintf("%s on %s asks for a secret named %q, which is not a name", sessName, hostName, n),
				Hint: resumeHintOrLs(h.Name, hint),
				Code: "bad_secret",
				Kind: KindUsage,
			}
		case slices.Contains(own, n), slices.Contains(passed, n):
			names = append(names, n)
		default:
			missing = append(missing, n)
		}
	}
	if len(missing) == 0 {
		return names, nil
	}
	flags := "--secret " + strings.Join(missing, " --secret ")
	hint := ""
	if addrOK {
		hint = fmt.Sprintf("%s resume %s %s (passes them, if you trust %s with them)", brand.Name, addr, flags, hostName)
	}
	return nil, &UserError{
		Msg:  fmt.Sprintf("%s on %s asks for secrets that hosts.toml does not give %s: %s", sessName, hostName, hostName, strings.Join(missing, ", ")),
		Hint: resumeHintOrLs(h.Name, hint),
		Code: "secrets_missing",
	}
}

func resumeOneLine(s string) string {
	return api.CleanText(strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ").Replace(s), 200)
}

func validResumeAddr(host, id string) (string, bool) {
	if !ValidHostName(host) || !api.ValidSessionID(id) {
		return "", false
	}
	address := host + "/" + id
	return address, ShellWord(address) == address
}

func resumeHintOrLs(host, hint string) string {
	if hint != "" && !strings.ContainsAny(hint, "\r\n\t") && api.CleanText(hint, 4000) == hint {
		return hint
	}
	if ValidHostName(host) {
		return brand.Name + " ls " + host + " (lists sessions to check the ID)"
	}
	return brand.Name + " ls (lists sessions to check the ID)"
}
