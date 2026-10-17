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
	if !c.HasFeature("resume") {
		return nil, c.tooOldToResume()
	}
	var r ResumeResult
	if err := c.Call(ctx, "resume", a, &r); err != nil {
		return nil, c.resumeError(err)
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
		Hint: brand.Name + " host add " + c.Host.Name + "   (updates the helper)"}
}

// ResumeSecrets picks the secrets that a resume of session s on host h
// fetches. Those are the names that the session asks for and that h's own
// secrets in hosts.toml list, and the names that the user passed. The
// host's manifest names the secrets, so a host could ask for any of them.
// A name that neither lists is an error, which says how to pass it. So is
// a name that no secret could have. The error shows it quoted, since it
// comes from the host and could hold escapes for the terminal.
func ResumeSecrets(h Host, s Session, passed []string) ([]string, error) {
	own := SecretNames(h, nil)
	var names, missing []string
	for _, n := range s.Secrets {
		switch {
		case !secretNameRE.MatchString(n):
			return nil, &UserError{Msg: fmt.Sprintf("%s on %s asks for a secret named %q, which is not a name", s.Name, h.Name, n),
				Hint: fmt.Sprintf("%s kill %s/%s, then start a new session", brand.Name, h.Name, s.ID)}
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
	return nil, &UserError{
		Msg:  fmt.Sprintf("%s on %s asks for secrets that hosts.toml does not give %s: %s", s.Name, h.Name, h.Name, strings.Join(missing, ", ")),
		Hint: fmt.Sprintf("%s resume %s/%s %s   (passes them, if you trust %s with them)", brand.Name, h.Name, s.ID, flags, h.Name),
	}
}
