package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// completionPolicyMetadata reads local bytes only. The shared decoder and
// merger supply names and stable IDs without selecting a repository or host.
func completionPolicyMetadata(withHosts bool) (client.PolicyCompletionMetadata, bool) {
	settings, ok := optionalCompletionPolicyFile("config.toml")
	if !ok {
		return client.PolicyCompletionMetadata{}, false
	}
	hosts := ""
	if withHosts {
		hosts, ok = optionalCompletionPolicyFile("hosts.toml")
		if !ok {
			return client.PolicyCompletionMetadata{}, false
		}
	}
	metadata, err := client.DecodePolicyCompletionMetadata(settings, hosts)
	return metadata, err == nil
}

func optionalCompletionPolicyFile(name string) (string, bool) {
	path := filepath.Join(client.ConfigDir(), name)
	data, ok := readCompletionFile(path)
	if ok {
		return string(data), true
	}
	// Missing input is an empty layer. Existing unusable input stays an error.
	// Lstat never opens the replacement path, including a FIFO or device.
	_, err := os.Lstat(path)
	return "", os.IsNotExist(err)
}

func completionProfileNames(names []string, prefix string) []string {
	var out []string
	for _, name := range names {
		if cleanCompMapKey(name) == name && strings.TrimSpace(name) == name && name != "" && strings.HasPrefix(name, prefix) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func completeRuleIDs(_ *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
	metadata, ok := completionPolicyMetadata(true)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return completionRuleRows(metadata.Rules, prefix), cobra.ShellCompDirectiveNoFileComp
}

func completionRuleRows(rules []api.Rule, prefix string) []string {
	var out []string
	for _, rule := range rules {
		if rule.ID == "" || cleanCompMapKey(rule.ID) != rule.ID || strings.TrimSpace(rule.ID) != rule.ID || !strings.HasPrefix(rule.ID, prefix) {
			continue
		}
		out = append(out, compItem(rule.ID, cleanCompDesc(rule.Target)))
	}
	// Equal IDs with different targets keep both descriptions. A truncated
	// stable hash must never choose one arbitrary configured rule for the user.
	slices.Sort(out)
	return slices.Compact(out)
}
