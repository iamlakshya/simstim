package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/grant"
)

func policyLsCmd() *cobra.Command {
	var layers policyLayerFlags
	var asJSON bool
	cmd := &cobra.Command{Use: "ls", Short: "Show the merged policy and each value's layer", Args: cobra.NoArgs, Example: "  " + brand.Name + " policy ls --host box --repo . --profile review",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withPolicy(cmd, layers, func(h client.Host, s client.Session, policy api.Policy) error {
				if asJSON {
					out := policyObject(h, s)
					out["policy"] = policy
					if s.Wall != nil {
						out["wall"] = s.Wall
					}
					return printJSON(out)
				}
				printPolicy(s, policy)
				return nil
			})
		}}
	layers.add(cmd)
	addJSON(cmd, &asJSON)
	return cmd
}

func policyCheckCmd() *cobra.Command {
	var layers policyLayerFlags
	var asJSON bool
	cmd := &cobra.Command{Use: "check <target>", Short: "Explain which rule and layer decides a destination", Args: cobra.ExactArgs(1), Example: "  " + brand.Name + " policy check api.example.com --in box/auth-fix",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withPolicy(cmd, layers, func(h client.Host, s client.Session, policy api.Policy) error {
				answer, err := grant.Decide(s.Agent, policy, args[0])
				if err != nil {
					return client.PolicyError(err)
				}
				if asJSON {
					out := policyObject(h, s)
					out["target"], out["decision"] = args[0], answer
					return printJSON(out)
				}
				decision := "deny"
				if answer.Allow {
					decision = "allow"
				}
				if answer.Unknown != "" {
					say(cmd, "%s: %s (no matching rule; network.unknown=%s, refused in this phase).\n", client.CleanLine(args[0]), decision, answer.Unknown)
					return nil
				}
				say(cmd, "%s: %s by %s in %s %s.\n", client.CleanLine(args[0]), decision, client.CleanLine(answer.Rule), answer.Layer, client.CleanLine(answer.Name))
				return nil
			})
		}}
	layers.add(cmd)
	addJSON(cmd, &asJSON)
	return cmd
}

func withPolicy(cmd *cobra.Command, layers policyLayerFlags, f func(client.Host, client.Session, api.Policy) error) error {
	if layers.in != "" {
		if layers.count() != 1 {
			return policyUsage("a session policy cannot be mixed with file layers", "Pass --in alone, or combine --host, --repo and --profile.", "layer_conflict")
		}
		return withSession(cmd, layers.in, func(ctx context.Context, h client.Host, c *client.Conn, s *client.Session) error {
			policy, err := c.Policy(ctx, s.ID)
			if err != nil {
				return err
			}
			return f(h, *s, policy)
		})
	}
	var h client.Host
	if layers.host != "" {
		var err error
		h, err = client.FindHost(layers.host)
		if err != nil {
			return err
		}
	}
	origin, err := client.RepoOrigin(cmd.Context(), layers.repo)
	if err != nil {
		return err
	}
	selected, err := client.ResolvePolicyLayers(client.LoadSettings(), h, origin, layers.profile, api.LayerValues{})
	if err != nil {
		return err
	}
	policy, _, err := grant.MergeLayers(selected)
	if err != nil {
		return client.PolicyError(err)
	}
	return f(h, client.Session{}, policy)
}

func printPolicy(s client.Session, policy api.Policy) {
	level := "none"
	if s.Wall != nil {
		level = client.CleanLine(s.Wall.Level)
	} else if policy.Sandbox.Wall != nil {
		level = *policy.Sandbox.Wall
	}
	fmt.Printf("Wall: %s\nPreset: %s\nUnknown: %s (unknown flows are refused in this phase)\n", client.CleanLine(level), client.CleanLine(string(policy.Preset)), client.CleanLine(string(policy.Unknown)))
	for _, lock := range policy.Locks {
		fmt.Printf("Lock: %s", client.CleanLine(strings.TrimSpace(string(lock.Layer)+" "+lock.Name)))
		if len(lock.Deny) > 0 {
			fmt.Printf(" deny %s", client.CleanLine(strings.Join(lock.Deny, ", ")))
		}
		fmt.Println()
		printPolicyValues(lock.LayerValues, nil, "  locked ")
	}
	printPolicyValues(policy.LayerValues, policy.Origins, "Value: ")
	for _, rule := range policy.Rules {
		until := "next start"
		if rule.Layer == api.LayerSession {
			until = "this session"
		}
		if rule.Once {
			until = "once"
		} else if !rule.Until.IsZero() {
			until = rule.Until.UTC().Format(time.RFC3339)
		}
		action := "deny"
		if rule.Allow {
			action = "allow"
		}
		name := rule.Name
		if rule.Source == api.SourceRepositoryFile {
			name += " (repository file)"
		}
		fmt.Printf("%s  %s %s %s  %s %s  %s\n", client.CleanLine(rule.ID), action, client.CleanLine(string(rule.Door)), client.CleanLine(rule.Target), client.CleanLine(string(rule.Layer)), client.CleanLine(name), until)
	}
}

func printPolicyValues(values api.LayerValues, origins []api.ValueOrigin, prefix string) {
	b, err := json.Marshal(values)
	if err != nil {
		return
	}
	var tables map[string]map[string]any
	if json.Unmarshal(b, &tables) != nil {
		return
	}
	for _, origin := range origins {
		table, key, ok := strings.Cut(origin.Key, ".")
		if !ok {
			continue
		}
		v := tables[table][key]
		label := origin.Key
		if origin.Item != "" {
			label += "[" + origin.Item + "]"
			if env, ok := v.(map[string]any); ok {
				v = env[origin.Item]
			} else {
				v = origin.Item
			}
		}
		value, err := json.Marshal(v)
		if err != nil {
			continue
		}
		source := strings.TrimSpace(string(origin.Layer) + " " + origin.Name + originSource(origin.Source))
		fmt.Printf("%s%s = %s  %s\n", prefix, client.CleanLine(label), client.CleanLine(string(value)), client.CleanLine(source))
	}
	if origins == nil {
		for _, table := range []string{"sandbox", "network", "classifier"} {
			for _, key := range slices.Sorted(maps.Keys(tables[table])) {
				value := tables[table][key]
				b, _ := json.Marshal(value)
				fmt.Printf("%s%s.%s = %s\n", prefix, table, client.CleanLine(key), client.CleanLine(string(b)))
			}
		}
	}
}

func originSource(source api.LayerSource) string {
	if source == api.SourceRepositoryFile {
		return " (repository file)"
	}
	return ""
}
