package cli

import (
	"maps"
	"strings"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func hostLabelFamilySetting() hostSetting {
	return hostSetting{key: "label.", prefix: true,
		about: "a note, such as label.location=home. An empty value removes that label.\nLabels never select a host or allow a grant.",
		check: func(client.Host, string) (string, error) { return "", badHostLabel() },
		now:   func(client.Host) string { return "" }}
}

// findHostLabelSetting binds only a registered prefix family. Exact settings
// have already been checked by findSetting. A label cannot name another key.
func findHostLabelSetting(settings []hostSetting, key string) *hostSetting {
	for _, setting := range settings {
		if setting.prefix && setting.key == "label." && strings.HasPrefix(key, setting.key) {
			return bindHostLabelSetting(key)
		}
	}
	return nil
}

func bindHostLabelSetting(key string) *hostSetting {
	if !strings.HasPrefix(key, "label.") {
		return nil
	}
	name := strings.TrimPrefix(key, "label.")
	return &hostSetting{
		key: key,
		check: func(_ client.Host, value string) (string, error) {
			if name == "" || strings.TrimSpace(name) == "" || !utf8.ValidString(name) || shown(name) != name || !utf8.ValidString(value) || shown(value) != value {
				return "", badHostLabel()
			}
			return value, nil
		},
		now: func(host client.Host) string {
			return shown(host.Labels[name])
		},
		save: func(host *client.Host, value string) {
			// prepareSetPairs starts with a shallow host copy. Keep the saved
			// entry's original label map intact for the membership comparison.
			labels := maps.Clone(host.Labels)
			if value == "" {
				delete(labels, name)
			} else {
				if labels == nil {
					labels = map[string]string{}
				}
				labels[name] = value
			}
			if len(labels) == 0 {
				labels = nil
			}
			host.Labels = labels
		},
	}
}

func badHostLabel() error {
	return &client.UserError{Msg: "a host label needs a visible key after label. and a value without control or hidden characters",
		Hint: brand.Name + " host set <host> label.location=home, or label.location= to remove it.", Code: "bad_setting", Kind: client.KindUsage}
}

// hostLabelsSuffix uses the same sorted, terminal-safe metadata view as host
// show and the launcher. It performs no dial and never participates in policy.
func hostLabelsSuffix(host client.Host) string {
	for _, detail := range (client.HostDetails{Host: host}).Saved() {
		if detail.Label == "Labels" {
			return "  [" + detail.Text() + "]"
		}
	}
	return ""
}
