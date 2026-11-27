package client

import (
	"net/url"
	"strings"
)

// TrackedGrantKind reports whether k is recorded in StateDir()/grants.json.
func TrackedGrantKind(k OutboundKind) bool {
	switch k {
	case OutboundSecret, OutboundSSHAgent, OutboundSSHKey,
		OutboundAlertToken, OutboundRepo, OutboundPaste, OutboundPush, OutboundPublicPush, OutboundRootRun:
		return true
	default:
		return false
	}
}

// RotatableGrantKind reports whether k names a credential that RotateSecrets
// reminds the user to rotate when a host is removed.
func RotatableGrantKind(k OutboundKind) bool {
	switch k {
	case OutboundSecret, OutboundSSHKey, OutboundAlertToken:
		return true
	default:
		return false
	}
}

// ShortHostID formats a valid 32-character hex host ID as "abcd…wxyz", or
// returns "unknown" when hostID is empty or malformed.
func ShortHostID(hostID string) string {
	if !ValidHostID(hostID) {
		return "unknown"
	}
	return hostID[:4] + "…" + hostID[28:]
}

// ValidAlertGrantLabel reports whether label is a sanitised alert credential
// label suitable for the grant ledger: "ntfy" or "webhook:<lower-host>", with
// no URL path, query, fragment, port or credentials.
func ValidAlertGrantLabel(label string) bool {
	if label == "ntfy" {
		return true
	}
	host, ok := strings.CutPrefix(label, "webhook:")
	if !ok || host == "" || host != strings.ToLower(host) {
		return false
	}
	if hasInvalidRepoRune(host) || strings.ContainsAny(host, "/?#@:\\[]") {
		return false
	}
	return validRepoHost(host)
}

func badAlertAddressError(msg string) error {
	return &UserError{
		Msg:  msg,
		Hint: "Give a valid https:// or http:// alert URL.",
		Code: "bad_alert",
		Kind: KindUsage,
	}
}

// AlertCredentialKind inspects an alert destination and optional token command,
// returning its sanitised ledger label ("ntfy" or "webhook:<lower-host>") and
// whether it carries a credential. Invalid URLs fail closed without echoing
// raw URLs or tokens.
func AlertCredentialKind(d AlertDestination, tokenCmd string) (label string, hasCred bool, err error) {
	explicitCred := strings.TrimSpace(tokenCmd) != "" ||
		d.Token != "" ||
		d.TokenFile != "" ||
		len(d.Headers) > 0

	switch d.Type {
	case "command":
		if explicitCred {
			return "", false, &UserError{
				Msg:  "command alert destinations do not take tokens or HTTP headers",
				Hint: "Use --token-cmd only with --ntfy.",
				Code: "bad_alert",
				Kind: KindUsage,
			}
		}
		return "", false, nil
	case "ntfy":
		urlCred, err := inspectNtfyURL(d.URL)
		if err != nil {
			return "", false, err
		}
		return "ntfy", explicitCred || urlCred, nil
	case "webhook":
		host, urlCred, err := inspectWebhookURL(d.URL)
		if err != nil {
			return "", false, err
		}
		format := strings.ToLower(strings.TrimSpace(d.Format))
		return "webhook:" + host, explicitCred || urlCred || format == "slack" || format == "discord", nil
	default:
		return "", false, &UserError{
			Msg:  "unknown alert destination type",
			Hint: "Use command, ntfy or webhook.",
			Code: "bad_alert",
			Kind: KindUsage,
		}
	}
}

func inspectNtfyURL(raw string) (hasCred bool, err error) {
	if raw == "" || hasInvalidRepoRune(raw) {
		return false, badAlertAddressError("ntfy topic or URL is invalid")
	}
	if !strings.Contains(raw, "://") {
		if strings.ContainsAny(raw, "?#@:\\") {
			return false, badAlertAddressError("ntfy topic must not contain query, fragment or credentials")
		}
		return false, nil
	}
	u, host, err := parseAlertHTTPURL(raw)
	if err != nil {
		return false, err
	}
	_ = host
	qCred, err := hasAlertCredentialQuery(u)
	if err != nil {
		return false, err
	}
	return u.User != nil || qCred, nil
}

func inspectWebhookURL(raw string) (host string, hasCred bool, err error) {
	u, host, err := parseAlertHTTPURL(raw)
	if err != nil {
		return "", false, err
	}
	qCred, err := hasAlertCredentialQuery(u)
	if err != nil {
		return "", false, err
	}
	cred := u.User != nil || qCred || isCapabilityWebhook(host, u.Path)
	return host, cred, nil
}

func parseAlertHTTPURL(raw string) (*url.URL, string, error) {
	if raw == "" || hasInvalidRepoRune(raw) {
		return nil, "", badAlertAddressError("alert URL is invalid")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, "", badAlertAddressError("alert URL could not be parsed")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return nil, "", badAlertAddressError("alert URL must use http or https")
	}
	if strings.Count(u.Host, "@") > 0 {
		return nil, "", badAlertAddressError("alert URL authority is ambiguous")
	}
	host := strings.ToLower(u.Hostname())
	if !validRepoHost(host) {
		return nil, "", badAlertAddressError("alert URL host name is invalid")
	}
	return u, host, nil
}

func hasAlertCredentialQuery(u *url.URL) (bool, error) {
	if u.RawQuery == "" {
		return false, nil
	}
	vals, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return false, badAlertAddressError("alert URL query string is malformed")
	}
	for k := range vals {
		switch strings.ToLower(k) {
		case "auth", "token", "access_token", "api_key", "apikey", "key", "secret", "sig", "signature",
			"password", "pass", "credential", "bearer", "code":
			return true, nil
		}
	}
	return false, nil
}

func isCapabilityWebhook(host, urlPath string) bool {
	lp := strings.ToLower(urlPath)
	switch {
	case host == "hooks.slack.com":
		return true
	case strings.HasSuffix(host, ".slack.com") && strings.HasPrefix(lp, "/services/"):
		return true
	case (host == "discord.com" || host == "discordapp.com" ||
		strings.HasSuffix(host, ".discord.com") || strings.HasSuffix(host, ".discordapp.com")) &&
		strings.HasPrefix(lp, "/api/webhooks"):
		return true
	case host == "webhook.office.com" || strings.HasSuffix(host, ".webhook.office.com") ||
		strings.HasSuffix(host, ".logic.azure.com"):
		return true
	case host == "api.telegram.org" && strings.HasPrefix(lp, "/bot"):
		return true
	case host == "chat.googleapis.com":
		return true
	case host == "events.pagerduty.com":
		return true
	case host == "hooks.zapier.com" || host == "maker.ifttt.com":
		return true
	case strings.HasPrefix(lp, "/hooks/") || strings.HasPrefix(lp, "/api/webhook/") ||
		strings.HasPrefix(lp, "/api/webhooks/"):
		return true
	default:
		return false
	}
}
