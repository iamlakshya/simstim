package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func policyCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "policy", Short: "Read and change sandbox policy at any layer", Args: cobra.NoArgs,
		Long:    "Policy has six layers, from broadest to narrowest: built-in, all sessions, host, repository, profile and session.\nThe narrowest matching layer decides. A deny wins inside that layer. Top-level and host locks win over every layer.\nLists add up; a leading ! removes an inherited item. Profiles follow repository sections.\nFile-layer changes reach running sandboxes on reachable hosts. Other sandbox settings apply at the next start. Live held network requests appear with session policy when the helper supports them.",
		Example: "  " + brand.Name + " policy allow api.example.com --repo .\n  " + brand.Name + " policy deny paste.example.net --all\n  " + brand.Name + " policy check api.example.com --host my-vps --profile review"}
	cmd.AddCommand(policyAllowCmd(), policyDenyCmd(), policyRmCmd(), policyLsCmd(), policyCheckCmd(), policyLogCmd(), policyApplyCmd(), policyEditCmd(), policyLearnCmd(), policyLockdownCmd(), policyRefuseCmdV1())
	return cmd
}

type policyLayerFlags struct {
	in, profile, repo, host string
	all                     bool
}

func (f *policyLayerFlags) add(cmd *cobra.Command) {
	addIn(cmd, &f.in, "the session whose policy to use")
	addProfile(cmd, &f.profile, "a profile in config.toml")
	_ = cmd.RegisterFlagCompletionFunc("profile", completeProfiles)
	cmd.Flags().StringVar(&f.repo, "repo", "", "a repository origin, or a local folder such as .")
	addHostFlag(cmd, &f.host, "a saved host's policy")
	addAll(cmd, &f.all, "the layer for all sessions")
}

func (f policyLayerFlags) count() int {
	n := 0
	for _, v := range []string{f.in, f.profile, f.repo, f.host} {
		if v != "" {
			n++
		}
	}
	if f.all {
		n++
	}
	return n
}

func (f *policyLayerFlags) mutation(cmd *cobra.Command) error {
	if f.count() > 1 {
		return policyUsage("a policy change needs exactly one layer", "Pass one of --in, --profile, --repo, --host or --all.", "layer_conflict")
	}
	if f.count() == 1 {
		return nil
	}
	if !inputOn(cmd) {
		return &client.UserError{Msg: "choose which layer gets the policy change", Hint: prompts(cmd).withFlags("--all"), Code: "layer_missing", Kind: client.KindNeedsAnswer}
	}
	answer, err := prompts(cmd).ask("Layer (all, host NAME, repo PATH, profile NAME, or in SESSION): ")
	if err != nil {
		return err
	}
	kind, value, _ := strings.Cut(strings.TrimSpace(answer), " ")
	switch kind {
	case "all":
		f.all = true
	case "host":
		f.host = strings.TrimSpace(value)
	case "repo":
		f.repo = strings.TrimSpace(value)
	case "profile":
		f.profile = strings.TrimSpace(value)
	case "in":
		f.in = strings.TrimSpace(value)
	}
	if f.count() != 1 {
		return policyUsage("the policy layer has no usable answer", "Pass a layer flag on the command line.", "layer_missing")
	}
	return nil
}

func (f policyLayerFlags) file(ctx context.Context) (client.FileLayer, error) {
	switch {
	case f.all:
		return client.FileLayer{Kind: api.LayerAll}, nil
	case f.host != "":
		return client.FileLayer{Kind: api.LayerHost, Name: f.host}, nil
	case f.profile != "":
		return client.FileLayer{Kind: api.LayerProfile, Name: f.profile}, nil
	case f.repo != "":
		name, err := client.RepoOrigin(ctx, f.repo)
		return client.FileLayer{Kind: api.LayerRepo, Name: name}, err
	default:
		return client.FileLayer{}, policyUsage("choose a file policy layer", "Pass --all, --host, --repo or --profile.", "layer_missing")
	}
}

// policyTargetDoors is shared by the actual parser and its completion metadata.
var policyTargetDoors = []api.Door{api.DoorNet, api.DoorPush, api.DoorSSHKey, api.DoorPort}

func policyTarget(args []string) (api.Door, string, error) {
	door := api.DoorNet
	if len(args) == 2 {
		door = api.Door(args[0])
	}
	if !slices.Contains(policyTargetDoors, door) {
		return "", "", policyUsage(fmt.Sprintf("the %q door is not available in this version", client.CleanLine(string(door))), "Pass net, port, push or ssh-key, or leave the door out for net.", "bad_door")
	}
	return door, args[len(args)-1], nil
}

func policyUntil(value string, now time.Time) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return time.Time{}, policyUsage(fmt.Sprintf("%q is not a positive rule duration", client.CleanLine(value)), "Pass --for with a duration such as 10m or 1h.", "bad_duration")
	}
	return now.Add(duration).UTC(), nil
}

func sessionLayer(target string) error {
	if strings.TrimSpace(target) != "" {
		return nil
	}
	return &client.UserError{Msg: "say which session gets the policy with --in", Hint: brand.Name + " policy ls --in <host>/<session>", Code: "layer_missing", Kind: client.KindNeedsAnswer}
}

func policyUsage(message, hint, code string) error {
	return &client.UserError{Msg: message, Hint: hint, Code: code, Kind: client.KindUsage}
}
func policyObject(h client.Host, s client.Session) map[string]any { return sessionObject(h.Name, s) }
