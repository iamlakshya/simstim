package host

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// How secrets reach a session (roadmap R6). The host stores no secret:
//
//  1. The client fetches each value on the laptop and sends it in the "new"
//     request, over the SSH channel.
//  2. New writes only the names into the manifest, starts the holder, and
//     hands the values to it over the session's socket (0600, in a 0700
//     folder), before the agent starts.
//  3. The holder adds them to the agent's environment, and to nothing else:
//     no file, log, command line or reply carries them.
//
// After a host restart nobody can send them again: the agent starts without
// them, says so in its terminal, and the session lists them as missing.

// secretsWait is how long a new holder waits for its secrets before it starts
// the agent without them.
const secretsWait = 30 * time.Second

var envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// reservedEnv are variables the session sets itself.
var reservedEnv = map[string]bool{"TERM": true, "COLORTERM": true, "TERM_PROGRAM": true, "TERM_PROGRAM_VERSION": true}

// checkSecrets checks the names and values of a new session's secrets, and
// returns the names, sorted. Its errors name the secret, never its value.
func checkSecrets(s map[string]api.Secret) ([]string, error) {
	names := make([]string, 0, len(s))
	for k, v := range s {
		switch {
		case !envNameRE.MatchString(k):
			return nil, UserError("bad_secret", fmt.Sprintf("%q cannot name an environment variable", k), "Use letters, digits and underscores, as in OPENAI_API_KEY.")
		case reservedEnv[k] || strings.HasPrefix(k, brand.EnvPrefix):
			return nil, UserError("bad_secret", fmt.Sprintf("the session sets %s itself, so it cannot be a secret", k), "Rename the secret in config.toml.")
		case strings.ContainsRune(string(v), 0):
			return nil, UserError("bad_secret", fmt.Sprintf("the value of %s holds a NUL byte, which no environment can carry", k), "Check what its command prints.")
		}
		names = append(names, k)
	}
	sort.Strings(names)
	return names, nil
}

// secretsMsg is the holder request that carries the values.
type secretsMsg struct {
	Env map[string]api.Secret `json:"env"`
}

// deliverSecrets hands a new session's secrets to its holder.
func deliverSecrets(id string, s map[string]api.Secret) error {
	if len(s) == 0 {
		return nil
	}
	if _, err := HolderRequest(id, "secrets", secretsMsg{Env: s}, 5*time.Second); err != nil {
		return fmt.Errorf("pass the secrets to the session: %w", err)
	}
	return nil
}

// secretsFailed is the error of a new session whose holder did not take its
// secrets. From inside a session, the holder refuses them, and a retry cannot
// help: that refusal comes back as it is.
func secretsFailed(err error) error {
	var pe *proto.Error
	if errors.As(err, &pe) && pe.Code == errInside.Code {
		return pe
	}
	return UserError("secrets_failed", "the session could not get its secrets, so it was removed", "Try again.")
}

// awaitSecrets waits on the holder's socket for the session's secrets and
// returns them as environment entries for the agent. Any other connection
// is told the session is starting. It returns nil after the timeout.
func (h *Holder) awaitSecrets(want []string, timeout time.Duration) []string {
	ul, ok := h.ln.(*net.UnixListener)
	if !ok {
		return nil
	}
	deadline := time.Now().Add(timeout)
	_ = ul.SetDeadline(deadline)
	defer func() { _ = ul.SetDeadline(time.Time{}) }()
	for {
		conn, err := ul.Accept()
		if err != nil {
			h.log.Warn("secrets did not arrive, starting without them", "waited", timeout)
			return nil
		}
		env, err := h.readSecrets(conn, want, deadline)
		conn.Close()
		if err == nil {
			h.log.Info("secrets received", "count", len(env))
			return env
		}
	}
}

// readSecrets serves one connection while the holder waits for its secrets.
// A program inside a session gets the same answers as later: a pong for a
// ping, and forbidden for anything else, the secrets too (holder_peer.go).
func (h *Holder) readSecrets(conn net.Conn, want []string, deadline time.Time) ([]string, error) {
	_ = conn.SetDeadline(minTime(deadline, time.Now().Add(3*time.Second)))
	r := bufio.NewReader(conn)
	w := proto.NewWriter(conn)
	if f, err := proto.Read(r); err != nil || f.Type != proto.THello {
		return nil, errors.New("no hello")
	}
	if err := w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "holder"}); err != nil {
		return nil, err
	}
	if !h.fullPeer(conn) {
		c := &hclient{conn: conn, w: w, access: accessPing}
		for {
			f, err := proto.Read(r)
			if err != nil || !h.serveLimited(c, f) {
				return nil, errors.New(errInside.Message)
			}
		}
	}
	f, err := proto.Read(r)
	if err != nil || f.Type != proto.TRequest {
		return nil, errors.New("no request")
	}
	var req proto.Request
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		return nil, fmt.Errorf("read the request: %w", err)
	}
	fail := func(code, msg string) error {
		_ = w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, Error: &proto.Error{Code: code, Message: msg, Hint: "Try again in a moment."}})
		return errors.New(msg)
	}
	if req.Op != "secrets" {
		return nil, fail("starting", "the session is starting")
	}
	var msg secretsMsg
	if err := json.Unmarshal(req.Args, &msg); err != nil {
		return nil, fail("bad_request", "the secrets are not valid JSON")
	}
	env := make([]string, 0, len(want))
	for _, k := range want {
		v, ok := msg.Env[k]
		if !ok {
			return nil, fail("bad_request", "the secret "+k+" is missing")
		}
		env = append(env, k+"="+string(v))
	}
	_ = w.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true, Result: json.RawMessage(`{"ok":true}`)})
	return env, nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// missingSecretsNote is the line the session's terminal shows before an agent
// that starts without its secrets, and why.
func missingSecretsNote(names []string, restored bool) string {
	why := "they did not arrive from the laptop"
	if restored {
		why = "after a host restart, nothing can fetch them from your laptop"
	}
	return fmt.Sprintf("[%s: the agent starts without its secrets (%s): %s. Start a new session to pass them again.]",
		brand.Name, strings.Join(names, ", "), why)
}

// secretsBeforeStart gets the secrets of a session that is about to start its
// agent, and records any that are missing. The caller has not started the
// agent yet, so it needs no lock.
func (h *Holder) secretsBeforeStart(restored bool) {
	want := h.m.Secrets
	h.m.MissingSecrets = nil
	if len(want) == 0 {
		return
	}
	if !restored {
		h.secretEnv = h.awaitSecrets(want, secretsWait)
	}
	if h.secretEnv == nil {
		h.m.MissingSecrets = append([]string(nil), want...)
		h.notes = append(h.notes, missingSecretsNote(want, restored))
	}
}
