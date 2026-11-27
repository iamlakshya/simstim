package client

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/brand"
)

// OutboundKind is a kind of thing that leaves the laptop for a host
// (docs/contributing/isolation-spec.md 14.8.2 and 14.15).
type OutboundKind string

// The outbound grant kinds of isolation spec 14.8.2. OutboundForward and
// OutboundPaste share row 6's tier rule while staying distinct for the ledger.
const (
	OutboundSecret         OutboundKind = "secret"
	OutboundSSHAgent       OutboundKind = "ssh-agent"
	OutboundSSHKey         OutboundKind = "ssh-key"
	OutboundAlertToken     OutboundKind = "alert-token"
	OutboundRepo           OutboundKind = "repo"
	OutboundForward        OutboundKind = "forward"
	OutboundPaste          OutboundKind = "paste"
	OutboundReverseForward OutboundKind = "reverse-forward"
	OutboundPool           OutboundKind = "pool"
	OutboundDefaultHost    OutboundKind = "default-host"
	OutboundRootRun        OutboundKind = "root-run"
	OutboundSilentUpgrade  OutboundKind = "silent-upgrade"
	OutboundImageFetchV1   OutboundKind = "image-fetch-v1"
	// OutboundNet is a policy net allowance already classified as public by
	// the canonical rule validator. Private targets use OutboundPrivateNet.
	OutboundNet        OutboundKind = "net"
	OutboundPrivateNet OutboundKind = "private-net"
	OutboundFloor      OutboundKind = "floor"
	OutboundTemplate   OutboundKind = "template"
	OutboundPush       OutboundKind = "push"
	OutboundPublicPush OutboundKind = "public-push"
)

// Valid reports whether k is one of the defined OutboundKind values.
func (k OutboundKind) Valid() bool {
	switch k {
	case OutboundSecret, OutboundSSHAgent, OutboundSSHKey, OutboundAlertToken,
		OutboundRepo, OutboundForward, OutboundPaste, OutboundReverseForward,
		OutboundPool, OutboundDefaultHost, OutboundRootRun, OutboundSilentUpgrade,
		OutboundNet, OutboundPrivateNet, OutboundFloor, OutboundTemplate, OutboundPush, OutboundPublicPush, OutboundImageFetchV1:
		return true
	default:
		return false
	}
}

// Outbound is one thing that may leave the laptop, by kind and name.
// It is not api.Rule, which opens a door inside a sandbox.
type Outbound struct {
	Kind      OutboundKind
	Name      string
	Confirmed bool
}

// ConfirmKind says how the CLI or launcher asks for confirmation when Allow
// returns a *ConfirmError.
type ConfirmKind string

// Confirmation question styles for ConfirmError.
const (
	// ConfirmYesNo asks a [y/N] question at an interactive terminal.
	ConfirmYesNo ConfirmKind = "yes-no"
	// ConfirmHostName asks the user to type the host's exact name.
	ConfirmHostName ConfirmKind = "host-name"
)

// ConfirmError reports that a grant requires an interactive terminal answer
// before proceeding (isolation spec 14.8.5). Unwrap returns Err, a KindRefused
// *UserError, so callers with input turned off treat it as a refusal.
type ConfirmError struct {
	Prompt string
	Kind   ConfirmKind
	Host   string
	Grant  Outbound
	Err    *UserError
}

func (e *ConfirmError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *ConfirmError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// ValidateSettings checks the complete configuration before an outbound grant.
func ValidateSettings(s Settings) error {
	_, err := NormaliseSettingsSecrets(s)
	return err
}

func validateSecretTiers(s Settings) error {
	if len(s.SecretTiers) == 0 {
		return nil
	}
	names := make([]string, 0, len(s.SecretTiers))
	for name := range s.SecretTiers {
		names = append(names, name)
	}
	sort.Strings(names)
	p := settingsPath()
	for _, name := range names {
		if checkSecretName(name, true) != secretNameOK {
			return &UserError{
				Msg:  fmt.Sprintf("%s: %q in [secret_tiers] cannot name a secret", p, CleanLine(name)),
				Hint: "Fix [secret_tiers] in " + p + ": secret names use letters, digits and underscores.",
				Code: "bad_config",
				Kind: KindUsage,
			}
		}
		if _, err := ParseTier(s.SecretTiers[name]); err != nil {
			return &UserError{
				Msg:  fmt.Sprintf("%s: %q is not a host tier for %s in [secret_tiers]", p, CleanLine(s.SecretTiers[name]), name),
				Hint: "Fix [secret_tiers] in " + p + ": use own, throwaway or shared.",
				Code: "bad_config",
				Kind: KindUsage,
			}
		}
	}
	return nil
}

// Allow says whether h, at tier t, may get o under settings s (isolation spec
// 14.8.2 and 14.8.5). A refusal is a *UserError with KindRefused; a grant that
// needs an interactive terminal question returns a *ConfirmError. Invalid
// settings, tiers, kinds or names fail closed.
func Allow(h Host, t Tier, o Outbound, s Settings) error {
	view, err := NormaliseSettingsSecrets(s)
	if err != nil {
		return err
	}
	return allowWithValidatedConfig(h, t, o, &s, &view, nil)
}

// allowWithValidatedConfig is the ONE outbound dispatch. The closed agent
// barrier follows complete validation without fabricating Settings or a
// secret view. It can enter only the existing SSH-agent row of this table.
func allowWithValidatedConfig(h Host, t Tier, o Outbound, s *Settings, view *SecretConfigView, barrier *agentSettingsBarrier) error {
	if s == nil || view == nil {
		if s != nil || view != nil || barrier == nil || o.Kind != OutboundSSHAgent {
			return validatedConfigScopeError()
		}
		if err := barrier.check(); err != nil {
			return err
		}
	} else if barrier != nil {
		return validatedConfigScopeError()
	}
	if h.Name != "" && !ValidHostName(h.Name) {
		return badHostNameError(h.Name)
	}
	if !t.Valid() {
		return &UserError{
			Msg:  hostDisplayName(h) + " has no tier yet, so the grant is refused",
			Hint: fmt.Sprintf("%s host set %s tier=own, or tier=throwaway, or tier=shared", brand.Name, hostCommandArg(h)),
			Code: "tier_refused",
			Kind: KindRefused,
		}
	}
	switch o.Kind {
	case OutboundSecret:
		return allowSecret(h, t, o, *s, *view, false)
	case OutboundSSHKey:
		return allowSecret(h, t, o, *s, *view, true)
	case OutboundSSHAgent:
		return allowSSHAgent(h, t, o)
	case OutboundAlertToken:
		return allowAlertToken(h, t, o)
	case OutboundRepo:
		return allowRepo(h, t, o)
	case OutboundPush, OutboundPublicPush:
		if !canonicalLedgerRepo(o.Name) {
			return badRepoError("push needs its exact canonical repository identity")
		}
		return allowCanonicalRepo(h, t, o.Name)
	case OutboundForward:
		return allowCleanOptionalName(o, "port forward")
	case OutboundPaste:
		return allowNameless(o, "paste")
	case OutboundReverseForward:
		return allowOwnOnly(h, t, o, "reverse port forward", "a reverse port forward",
			fmt.Sprintf("Use %s forward without --reverse, or use an own host.", brand.Name))
	case OutboundPool:
		return allowPool(h, t, o)
	case OutboundDefaultHost:
		return allowDefaultHost(h, t, o)
	case OutboundRootRun:
		return allowRootRun(h, t, o)
	case OutboundImageFetchV1:
		return allowImageFetchV1(h, t, o)
	case OutboundSilentUpgrade:
		return allowSilentUpgrade(h, t, o)
	case OutboundNet:
		if strings.TrimSpace(o.Name) == "" || hasControlRune(o.Name) {
			return &UserError{Msg: "a public network grant needs a canonical target",
				Hint: "Choose a valid public destination and port.", Code: "bad_grant", Kind: KindUsage}
		}
		return nil
	case OutboundPrivateNet:
		return allowOwnOnly(h, t, o, "private network grant", "a private network or LAN grant",
			"Allow only public hostnames on this host, or run the session on an own host.")
	case OutboundFloor:
		return allowFloor(h, t, o)
	case OutboundTemplate:
		return allowTemplate(h, t, o)
	default:
		return &UserError{
			Msg:  fmt.Sprintf("unknown outbound grant kind %q", CleanLine(string(o.Kind))),
			Hint: "Update " + brand.Name + " and try again.",
			Code: "grant_refused",
			Kind: KindRefused,
		}
	}
}

func hostDisplayName(h Host) string {
	if h.Name == "" {
		return "this host"
	}
	return CleanLine(h.Name)
}

func hostCommandArg(h Host) string {
	if h.Name == "" {
		return "<host>"
	}
	return ShellQuote(CleanLine(h.Name))
}

func hasControlRune(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func agentTokenHint(secretName string) string {
	for _, a := range agents.Table {
		if a.Setup != nil && a.TokenHint != "" && slices.Contains(a.Setup.LoginEnv, secretName) {
			return a.TokenHint
		}
	}
	return ""
}

func allowSecret(h Host, t Tier, o Outbound, s Settings, view SecretConfigView, sshKey bool) error {
	if o.Name == "SSH_AUTH_SOCK" {
		return &UserError{Msg: "the session sets SSH_AUTH_SOCK itself, so it cannot be a secret",
			Hint: "Use --ssh-agent for one unwalled session on a host you own.", Code: "bad_secret", Kind: KindUsage}
	}
	if checkSecretName(o.Name, true) != secretNameOK {
		return &UserError{
			Msg:  fmt.Sprintf("%q cannot name a secret", CleanLine(o.Name)),
			Hint: "Use letters, digits and underscores, as in OPENAI_API_KEY.",
			Code: "bad_secret",
			Kind: KindUsage,
		}
	}
	if err := allowSecretOnHost(h, o.Name, view); err != nil {
		return err
	}
	minTier := TierOwn
	if raw, ok := s.SecretTiers[o.Name]; ok {
		minTier, _ = ParseTier(raw)
	}
	hName := hostDisplayName(h)
	if t < minTier {
		hint := fmt.Sprintf("Make a low-limit token for %s, and mark it under [secret_tiers] in %s.", hName, settingsPath())
		if sshKey {
			hint = fmt.Sprintf("Make a dedicated deploy key for %s, and mark it under [secret_tiers] in %s.", hName, settingsPath())
		} else if extra := agentTokenHint(o.Name); extra != "" {
			hint = extra + " Mark the scoped secret under [secret_tiers] in " + settingsPath() + "."
		}
		return &UserError{
			Msg:  fmt.Sprintf("%s is a %s host, so it does not get %s", hName, t, o.Name),
			Hint: hint,
			Code: "tier_refused",
			Kind: KindRefused,
		}
	}
	if t == TierShared && !o.Confirmed {
		return &ConfirmError{
			Prompt: fmt.Sprintf("Send %s to %s? Its owner can read it.", o.Name, hName),
			Kind:   ConfirmYesNo,
			Host:   hName,
			Grant:  o,
			Err: &UserError{
				Msg:  fmt.Sprintf("%s is a shared host, so sending %s needs confirmation in a terminal", hName, o.Name),
				Hint: fmt.Sprintf("Run this command in an interactive terminal without --no-input to confirm sending %s to %s.", o.Name, hName),
				Code: "tier_refused",
				Kind: KindRefused,
			},
		}
	}
	return nil
}

func allowNameless(o Outbound, label string) error {
	if o.Name != "" {
		return &UserError{
			Msg:  label + " grants take no name",
			Hint: "Leave the grant name empty.",
			Code: "bad_grant",
			Kind: KindUsage,
		}
	}
	return nil
}

func allowCleanOptionalName(o Outbound, label string) error {
	if hasControlRune(o.Name) {
		return &UserError{
			Msg:  label + " grant name contains control characters",
			Hint: "Remove control characters from the grant name.",
			Code: "bad_grant",
			Kind: KindUsage,
		}
	}
	return nil
}

func allowSSHAgent(h Host, t Tier, o Outbound) error {
	if err := allowNameless(o, "ssh-agent"); err != nil {
		return err
	}
	if t == TierOwn {
		return nil
	}
	hName := hostDisplayName(h)
	return &UserError{
		Msg:  fmt.Sprintf("%s is a %s host, so it does not get your SSH agent", hName, t),
		Hint: fmt.Sprintf("Use a deploy key made for %s, or bring the work back with %s pull.", hName, brand.Name),
		Code: "tier_refused",
		Kind: KindRefused,
	}
}

func allowAlertToken(h Host, t Tier, o Outbound) error {
	if o.Name != "" && !ValidAlertGrantLabel(o.Name) {
		return &UserError{
			Msg:  "alert token grant label is invalid",
			Hint: "Use ntfy or webhook:<hostname>, without a URL path, query or token.",
			Code: "bad_grant",
			Kind: KindUsage,
		}
	}
	if t == TierOwn {
		return nil
	}
	hName := hostDisplayName(h)
	return &UserError{
		Msg:  fmt.Sprintf("%s is a %s host, so it does not get an alert token", hName, t),
		Hint: fmt.Sprintf("%s host alerts set %s --ntfy <topic> without a token.", brand.Name, hostCommandArg(h)),
		Code: "tier_refused",
		Kind: KindRefused,
	}
}

func allowRepo(h Host, t Tier, o Outbound) error {
	normRepo, err := NormalizeRepo(o.Name)
	if err != nil {
		return err
	}
	return allowCanonicalRepo(h, t, normRepo)
}

func allowCanonicalRepo(h Host, t Tier, normRepo string) error {
	if t == TierOwn || t == TierThrowaway {
		return nil
	}
	hName := hostDisplayName(h)
	matched := false
	for _, pat := range h.Repos {
		prefix, wildcard, err := ValidateRepoPattern(pat)
		if err != nil {
			return &UserError{
				Msg:  fmt.Sprintf("hosts.toml: invalid repository pattern in repos of %s: %s", hName, err.Error()),
				Hint: fmt.Sprintf("Fix repos for %s in %s.", hName, hostsPath()),
				Code: "bad_config",
				Kind: KindUsage,
			}
		}
		if matchCanonicalRepoPattern(canonicalRepo(prefix), wildcard, canonicalRepo(normRepo)) {
			matched = true
		}
	}
	if matched {
		return nil
	}
	return &UserError{
		Msg:  fmt.Sprintf("%s may not get %s", hName, normRepo),
		Hint: fmt.Sprintf("Add a pattern to the repos of %s in %s, for example %q.", hName, hostsPath(), normRepo),
		Code: "tier_refused",
		Kind: KindRefused,
	}
}

func allowOwnOnly(h Host, t Tier, o Outbound, label, what, hint string) error {
	if err := allowCleanOptionalName(o, label); err != nil {
		return err
	}
	if t == TierOwn {
		return nil
	}
	return &UserError{
		Msg:  fmt.Sprintf("%s is a %s host, so it does not get %s", hostDisplayName(h), t, what),
		Hint: hint,
		Code: "tier_refused",
		Kind: KindRefused,
	}
}

func validatedConfigScopeError() error {
	return &UserError{Msg: "this validation can approve only its original outbound grant kind",
		Hint: "Use complete current local settings validation for this grant.", Code: "grant_scope", Kind: KindRefused}
}
