package client

import (
	"context"
	"errors"
	"fmt"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"io"
	"regexp"
	"strings"
)

// The pin on the way to a host: the first connection pins the key of a
// saved host that has none, no other connection to a saved host goes
// without a pin, and a refusal of the pin becomes the error of spec 14.14.

// PinOptions say how a command that adds a host, such as host add, pins
// its key at the first connection (spec 14.7.1).
type PinOptions struct {
	// Want is the SHA256 fingerprint that --host-key gives, or "".
	Want string
	// Wants names every explicitly compared key of a checked join reply.
	// Want may coexist only when Wants is its identical single value.
	Wants []string
	// FirstUse says what to do with a key that neither the user's known
	// hosts nor Want names.
	FirstUse FirstUse
	// Replace pins again although the host has a pin: for a host that is
	// not saved yet, whose pin, if any, is left over. A saved host keeps
	// its pin: only host rekey replaces it.
	Replace bool
	// Pinned gets the pin, kept or new, when the host is reached over SSH.
	Pinned *Pin
}

type (
	pinKey    struct{}
	noticeKey struct{}
)

// WithPin returns a context in which the first connection to a host pins
// its key as o says, instead of as for a saved host.
func WithPin(ctx context.Context, o *PinOptions) context.Context {
	return context.WithValue(ctx, pinKey{}, o)
}

// WithNotices returns a context in which the client writes its notes on
// w, such as the pin that a host saved before pins gets at its next
// connection. Without it, they go to the debug log only, as in the
// launcher, which owns the terminal.
func WithNotices(ctx context.Context, w io.Writer) context.Context {
	return context.WithValue(ctx, noticeKey{}, w)
}

// notice writes msg to the notes of ctx, and to the debug log.
func notice(ctx context.Context, msg string) {
	DebugLog().Info(msg)
	if w, _ := ctx.Value(noticeKey{}).(io.Writer); w != nil {
		fmt.Fprintln(w, msg)
	}
}

// pinRoute pins h's key before the first connection through t, when t is
// ssh. Docker and E2B hosts have no host key.
func pinRoute(ctx context.Context, h Host, t Transport, interactive bool) (returned error) {
	if err := checkHostTransportAction(ctx, h); err != nil {
		return err
	}
	s, ok := t.(*SSH)
	if !ok || s.Host.SSH == "" {
		return nil
	}
	ctx, finishNative, err := s.beginNativeSource(ctx)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, finishNative()) }()
	if o, ok := ctx.Value(pinKey{}).(*PinOptions); ok && o != nil {
		return pinAsAsked(ctx, s.Host, o)
	}
	return pinSaved(s.policyContext(ctx), s.Host, h, interactive)
}

// pinAsAsked pins h's key as o says. A pin that h has already stays,
// unless o replaces it, and must hold o.Want: only host rekey changes a pin
// to another key.
func pinAsAsked(ctx context.Context, h Host, o *PinOptions) error {
	if err := checkFingerprintSet(o.Want, o.Wants); err != nil {
		return err
	}
	var p Pin
	var err error
	if !o.Replace && pinned(h.Name) {
		p, err = keptPinSet(ctx, h.Name, o.Want, o.Wants)
	} else {
		p, err = pinHostKeySet(ctx, h, o.Want, o.Wants, o.FirstUse, o.Replace)
	}
	if err != nil {
		return err
	}
	if o.Pinned != nil {
		*o.Pinned = p
	}
	return nil
}

// pinSaved pins the key of a saved host that has no pin, such as one saved
// before pins: once, at its next connection. When the user's own SSH
// settings trust the key that the host shows, it pins that key. Otherwise,
// it pins the key on first use where the user's config takes new keys
// without a question, or for a machine that the client made, which nobody
// else had. Otherwise ssh asks at a terminal, and without one the
// connection fails with exit code 4. Every failure stops the connection: a
// saved host is never reached without a pin. A host that is not saved yet
// gets no pin here: the command that adds it pins it. A hosts.toml that
// cannot be read is an error, never a host that is not saved.
//
// It says where the pin came from in the notes of ctx only for a
// connection that may use the terminal. A connection in the background,
// such as an attach's reconnect, writes nothing to the terminal.
// h is the SSH route; original is the saved entry that selected that route.
// A provider's target does not replace the machine's saved CAS identity.
func pinSaved(ctx context.Context, h, original Host, interactive bool) error {
	made := original.Machine != nil
	if err := ctx.Err(); err != nil {
		return err
	}
	if attempt, _ := ctx.Value(hostTransportAttemptKey{}).(*hostTransportAttempt); attempt != nil && attempt.kind == hostTransportProvisional {
		saved, err := SavedEntry(h.Name)
		if err != nil {
			return err
		}
		if saved != nil {
			return hostStateRemovalError("a provisional read-only transport cannot acquire a saved host pin")
		}
		return nil // Still-unsaved reads acquire no saved pin or migration authority.
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	if !interactive {
		ctx = quietConnectionNotes(ctx)
	}
	if !ValidHostName(h.Name) {
		return nil // writeWrapper refuses it: no connection starts
	}
	if err := migratePinContext(ctx, h.Name); err != nil {
		return err
	}
	if err := checkTierKeyPin(ctx, h.Name); err != nil {
		return err
	}
	if pinned(h.Name) {
		return nil
	}
	if ok, err := savedHost(h.Name); err != nil || !ok {
		return err
	}
	mode := FirstUseAccept
	if !made {
		vals, err := hostKeyValues(ctx, h)
		if err != nil {
			return err
		}
		switch {
		case takesNewKeys(first(vals, "stricthostkeychecking")):
		case interactive && InputAllowed(ctx):
			mode = FirstUseAsk
			notice(ctx, h.Name+" has no pinned host key yet. If your known_hosts lacks it, ssh shows the key and asks once.")
		default:
			mode = FirstUseRefuse
		}
	}
	ctx, endMutation, err := holdSavedPinMutation(ctx, original, "pin the saved host key")
	if err != nil {
		return err
	}
	defer endMutation()
	p, err := pinHostKey(ctx, h, "", mode, false)
	var ue *UserError
	switch {
	case errors.Is(err, ErrUnchecked):
		return &UserError{
			Msg:  h.Name + " has no pinned host key yet, and your known_hosts lacks its key, so the client does not connect without a question",
			Hint: fmt.Sprintf("%s host rekey %s --host-key SHA256:<fingerprint> --confirm %s", brand.Name, h.Name, h.Name),
			Code: "needs_answer", Kind: KindNeedsAnswer,
		}
	case errors.As(err, &ue) && ue.Code == "host_key_changed":
		return &UserError{Msg: ue.Msg, Hint: rekeyHint(h.Name), Code: ue.Code, Kind: ue.Kind}
	case err != nil:
		return err
	}
	if err := recordPinnedKey(ctx, original, p.Fingerprint); err != nil {
		return err
	}
	if p.Source != PinKept {
		line := h.Name + ": " + p.Line()
		if p.Source == PinFirstUse && !made {
			line += ". Check it on the machine: " + p.CheckCommand()
		}
		notice(ctx, line)
	}
	return nil
}

// rekeyHint is the hint of a key that the pin, or the user's known hosts,
// refuse.
func rekeyHint(name string) string {
	return fmt.Sprintf("If you reinstalled %s: %s host rekey %s. Otherwise, do not connect.", name, brand.Name, name)
}

// needsPin reports whether h is reached over SSH and has no pin yet, own
// or staged. No ssh command logs in to such a host (connArgs). A pin of
// the first version moves to its place first (migratePin).
func needsPin(ctx context.Context, h Host) (bool, error) {
	if _, err := stageForContext(ctx, h.Name); err != nil {
		return false, err
	}
	if h.SSH == "" || !ValidHostName(h.Name) {
		return false, nil
	}
	if err := migratePinContext(ctx, h.Name); err != nil {
		return false, err
	}
	return !pinned(h.Name), nil
}

// connArgs are args for a command that can log in to the host. Every such
// command checks a pin: the host's own, for a saved host, or one that this
// process staged for it while it adds it. It builds its options from one
// snapshot of the pin and the host list, read under the pin's lock, so the
// check that the host has a pin and the options that check it never differ:
// a pin that another client removes after the snapshot leaves ssh with a pin
// file that is gone, which it refuses, and a published pin whose host was
// removed from hosts.toml is refused too.
func (s *SSH) connArgs(ctx context.Context, shared bool) ([]string, error) {
	ctx = s.policyContext(ctx)
	if err := s.checkNativeSourceScope(ctx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.Host.SSH == "" || !ValidHostName(s.Host.Name) {
		return s.argsContext(ctx, shared) // writeWrapper refuses a bad name
	}
	if _, err := stageForContext(ctx, s.Host.Name); err != nil {
		return nil, err
	}
	if err := migratePinContext(ctx, s.Host.Name); err != nil {
		return nil, err
	}
	var snap pinSnap
	var ok, isSaved bool
	var err, serr error
	var args []string
	if lerr := readPinLocked(ctx, s.Host.Name, func() {
		if _, serr = stageForContext(ctx, s.Host.Name); serr != nil {
			return
		}
		snap, ok, err = loadPin(s.Host.Name)
		if err == nil && (!ok || !snap.staged) {
			isSaved, serr = savedHost(s.Host.Name)
		}
		if err == nil && serr == nil && ok && (snap.staged || isSaved) {
			args, err = s.argsWithContext(ctx, &snap, shared)
		}
	}); lerr != nil {
		return nil, lerr
	}
	if err != nil {
		return nil, err
	}
	if serr != nil {
		return nil, serr
	}
	if ok && (snap.staged || isSaved) {
		if err := s.checkSSHEnvironment(ctx, args); err != nil {
			return nil, err
		}
		return args, nil
	}
	if isSaved {
		return nil, &UserError{
			Msg:  s.Host.Name + " has no pinned host key yet, so the client does not connect to it",
			Hint: brand.Name + " host test " + s.Host.Name + " (it pins the key first)",
			Code: "host_key_unpinned",
		}
	}
	return nil, &UserError{
		Msg:  s.Host.Name + " is not a saved host with a pinned host key, so the client does not connect to it",
		Hint: brand.Name + " host ls (a host that you forgot needs host add again)",
		Code: "host_key_unpinned",
	}
}

// answers checks that the host behind t answers, for a machine that has
// just started. A host with no pin yet is asked for its key, on a
// connection that logs in to nothing, so that no login goes without a pin.
func answers(ctx context.Context, t Transport) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s, ok := t.(*SSH); ok {
		needed, err := needsPin(ctx, s.Host)
		if err != nil {
			return err
		}
		if needed {
			_, err := fetchHostKey(ctx, s.Host, false, "")
			return err
		}
	}
	_, err := t.Run(ctx, "true", nil)
	return err
}

// What ssh writes to its own -E log when the pin stops a connection
// (OpenSSH's sshconnect.c), with LogLevel=ERROR, which hides a host's
// banner. Each pattern is a whole line, and the log must end at "Host key
// verification failed." with exit code 255.
var (
	changedRE = regexp.MustCompile(`(?m)^Host key for (\S+) has changed and you have requested strict checking\.\r?$`)
	unknownRE = regexp.MustCompile(`(?m)^No ([A-Za-z0-9-]+) host key is known for (\S+) and you have requested strict checking\.\r?$`)
	shownRE   = regexp.MustCompile(`(?m)^The fingerprint for the ([A-Za-z0-9-]+) key sent by the remote host is\r?\n(SHA256:[A-Za-z0-9+/]{43})\.?\r?$`)
)

// hostKeyError is the error of a connection to h that its pin stopped, read
// only from the destination ssh's own -E log and exit code (spec 14.14), or
// nil when the pin did not stop it.
func hostKeyError(h Host, sshLog string, code int) *UserError {
	if code != 255 || !pinned(h.Name) {
		return nil
	}
	shownType, shown, ok := parseHostKeyStop(sshLog, pinAlias(h.Name))
	if !ok {
		return nil
	}
	return hostKeyStopError(h, shownType, shown)
}

// hostKeyStopError formats a proven pin stop from either complete parser.
func hostKeyStopError(h Host, shownType, shown string) *UserError {
	if !pinned(h.Name) {
		return nil
	}
	hint := rekeyHint(h.Name)
	var keys []hostKey
	var err error
	if st, ok := stagedPin(h.Name); ok {
		keys = st.keys
	} else if keys, err = readPin(h.Name); err != nil {
		var ue *UserError
		if isForeign(err) && errors.As(err, &ue) {
			return ue
		}
		return &UserError{Msg: fmt.Sprintf("%s's pinned host key cannot be read, so the client does not connect: %v", h.Name, err),
			Hint: hint, Code: "host_key_unreadable", Kind: KindRefused}
	}
	pinnedFP := pinOf(keys, "").Fingerprint
	for _, k := range keys {
		if keyFamily(k.Type) == shownType {
			pinnedFP = k.Fingerprint()
			break
		}
	}
	msg := fmt.Sprintf("%s's host key changed: %s was pinned, and the host shows %s", h.Name, pinnedFP, shown)
	switch {
	case shown != "":
	case shownType == "?":
		msg = fmt.Sprintf("%s's host key changed: %s was pinned, and the host shows another key", h.Name, pinnedFP)
	default:
		msg = fmt.Sprintf("%s's host key changed: %s was pinned, and the host shows a key of another type (%s)", h.Name, pinnedFP, shownType)
	}
	return &UserError{Msg: msg, Hint: hint, Code: "host_key_changed", Kind: KindRefused}
}

func parseHostKeyStop(sshLog, alias string) (shownType, shown string, ok bool) {
	last := ""
	for _, l := range strings.Split(sshLog, "\n") {
		l = strings.TrimRight(l, "\r")
		if strings.TrimSpace(l) != "" {
			last = l
		}
		if authRE.MatchString(l) || deniedRE.MatchString(l) {
			return "", "", false
		}
	}
	if !failedRE.MatchString(last) {
		return "", "", false
	}
	if cm := changedRE.FindAllStringSubmatch(sshLog, -1); len(cm) > 0 {
		for _, m := range cm {
			if !strings.EqualFold(m[1], alias) {
				return "", "", false
			}
		}
		if sm := shownRE.FindAllStringSubmatch(sshLog, -1); len(sm) == 1 {
			return sm[0][1], sm[0][2], true
		}
		return "?", "", true
	}
	um := unknownRE.FindAllStringSubmatch(sshLog, -1)
	if len(um) == 0 {
		return "", "", false
	}
	for _, m := range um {
		if !strings.EqualFold(m[2], alias) {
			return "", "", false
		}
	}
	if len(um) == 1 {
		return um[0][1], "", true
	}
	return "?", "", true
}

// IsHostKeyError reports whether err is a refusal of a host's pinned key.
func IsHostKeyError(err error) bool {
	var ue *UserError
	return errors.As(err, &ue) && (ue.Code == "host_key_changed" || ue.Code == "host_key_unreadable")
}

// PinCheck is host doctor's line about h's pinned host key, which it runs
// on this computer. ok is false for a host that is not reached over SSH.
func PinCheck(h Host) (c api.Check, ok bool) {
	c, ok, _ = PinCheckContext(context.Background(), h)
	return c, ok
}

// PinCheckContext checks the local pin and returns cancellation errors.
// An interrupted check proposes no repair.
func PinCheckContext(ctx context.Context, h Host) (c api.Check, ok bool, err error) {
	if err := migratePinContext(ctx, h.Name); err != nil {
		return api.Check{}, false, err
	}
	c = api.Check{Name: "host key"}
	if !HasPin(h.Name) {
		if h.Machine != nil || h.SSH == "" {
			return c, false, nil // a machine reached without SSH
		}
		wantKey, err := recordedTierKey(h.Name)
		if err != nil {
			return api.Check{}, false, err
		}
		if wantKey != "" {
			c.Detail = "the pin file is missing (" + wantKey + " was pinned)"
			c.Fix = brand.Name + " host rekey " + h.Name
			return c, true, nil
		}
		c.Detail = "no pinned host key yet"
		c.Fix = brand.Name + " host test " + h.Name
		return c, true, nil
	}
	p, err := PinOf(h.Name)
	var ue *UserError
	switch {
	case isForeign(err) && errors.As(err, &ue):
		c.Detail, c.Fix = CleanLine(ue.Msg), ue.Hint
		return c, true, nil
	case err != nil:
		c.Detail = "the pin cannot be read: " + CleanLine(err.Error())
		c.Fix = brand.Name + " host rekey " + h.Name
		return c, true, nil
	}
	c.OK, c.Detail = true, p.Fingerprint+" pinned"
	return c, true, nil
}
