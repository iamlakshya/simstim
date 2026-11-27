package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"reflect"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// host show (docs/contributing/cli-spec.md 11.2) describes one host. It
// is the command of the launcher's host details, and takes its lines from
// the same function, client.HostDetails.

func hostShowCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show <host>",
		Short: "Describe a host",
		Long: "Describe one host, as the launcher's host details do: the saved entry, then what the host\n" +
			"says about itself. It does not start a stopped host. A host that does not answer is not a\n" +
			"failure: show prints the saved part, then why.",
		Example:           "  " + brand.Name + " host show my-vps\n  " + brand.Name + " host show my-vps --json",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeHostArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			h, err := client.FindHost(args[0])
			if err != nil {
				return err
			}
			hosts, err := client.LoadHosts()
			if err != nil {
				return err
			}
			tiers, err := client.LoadTiersContext(cmd.Context(), hosts)
			if err != nil {
				return err
			}
			d, err := lookAtHost(cmd.Context(), h, hosts, tiers)
			if cancelled := cmd.Context().Err(); cancelled != nil {
				return cancelled
			}
			if asJSON {
				return printJSON(hostShowObject(d, err))
			}
			showText(os.Stdout, os.Stderr, d, err)
			return nil
		},
	}
	addJSON(cmd, &asJSON)
	return cmd
}

// lookAtHost dials h for up to 30 seconds, and reads what the host says
// about itself, and how many sessions it has. It neither wakes the host
// nor installs or prunes a helper. The error says why the host did not
// answer.
func lookAtHost(ctx context.Context, h client.Host, hosts []client.Host, tiers client.Tiers) (client.HostDetails, error) {
	d := client.HostDetails{Host: h, Tier: tiers.Of(h.Name), IDChanged: tiers.IDChanged(h.Name)}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := client.Dial(client.WithoutInstall(client.WithoutWake(ctx)), h, true)
	if err != nil {
		return d, err
	}
	defer conn.Close()
	updated, terr := client.LoadTiersContext(ctx, hosts)
	if terr != nil {
		return d, terr
	}
	d.Tier, d.IDChanged = updated.Of(h.Name), updated.IDChanged(h.Name)
	info, err := conn.Doctor(ctx)
	if err != nil {
		return d, err
	}
	sessions, err := conn.Sessions(ctx)
	if err != nil {
		return d, err
	}
	d.Info, d.Sessions = &info, len(sessions)
	return d, nil
}

// showText writes the details of a host as text on w. When the host did
// not answer, the fix, such as host upgrade for a missing helper, goes on
// errW: it is a message, so it goes to stderr.
func showText(w, errW io.Writer, d client.HostDetails, err error) {
	printHost(w, d, err)
	if err != nil {
		fmt.Fprintf(errW, "Try: %s\n", shown(lookHint(d.Host.Name, err)))
	}
}

// printHost writes the details of a host as text: its name, a line for
// each detail, and why it did not answer, if it did not.
func printHost(w io.Writer, d client.HostDetails, err error) {
	fmt.Fprintln(w, shown(d.Host.Name))
	for _, l := range d.Lines() {
		fmt.Fprintf(w, "  %-11s%s\n", l.Label, l.Text())
	}
	if err != nil {
		fmt.Fprintf(w, "%s did not answer: %s\n", shown(d.Host.Name), shown(err.Error()))
	}
}

// hostShowObject is a host for scripts (cli-spec 11.2): its saved entry,
// with the keys in snake_case as `host ls --json` prints them, whether it
// answered, why not when it did not, and what it said, as `host doctor
// --json` prints it. session_count is the count of its sessions that the
// launcher's details show with the limits.
func hostShowObject(d client.HostDetails, err error) map[string]any {
	obj, ok := tomlKeys(reflect.ValueOf(d.Host)).(map[string]any)
	if !ok {
		obj = map[string]any{}
	}
	if d.Tier.Valid() {
		obj["tier"] = d.Tier.String()
	}
	if d.IDChanged {
		obj["host_id_changed"] = true
	}
	obj["reachable"] = err == nil
	if err != nil {
		obj["error"] = shown(err.Error())
		obj["try"] = shown(lookHint(d.Host.Name, err))
	}
	if d.Info != nil {
		obj["info"] = hostInfoJSON{d.Info}
		obj["session_count"] = d.Sessions
	}
	return obj
}

// hostInfoJSON keeps the host's data and integer precision while omitting
// unset fields, as host doctor's JSON does (cli-spec 7.2 and 11.2).
// RawMessage keeps strings and numbers exact through the outer JSON writer.
type hostInfoJSON struct{ info *client.HostInfo }

func (h hostInfoJSON) MarshalJSON() ([]byte, error) {
	raw, err := json.Marshal(h.info)
	if err != nil {
		return nil, fmt.Errorf("encode host info: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("read encoded host info: %w", err)
	}
	for key, value := range fields {
		switch string(value) {
		case "null", `""`:
			delete(fields, key)
		}
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("encode host info fields: %w", err)
	}
	return out, nil
}

// lookHint is the Try: line of a look at host name that failed: the
// error's own, or, for a plain error such as a timeout or a closed
// connection, host doctor, which says why the host did not answer.
func lookHint(name string, err error) string {
	if hint := hintOf(err); hint != "" {
		return hint
	}
	return brand.Name + " host doctor " + shellWord(name)
}

// hintOf is the Try: line of err, or "" for an error without one.
func hintOf(err error) string {
	var ue *client.UserError
	if errors.As(err, &ue) {
		return ue.Hint
	}
	return ""
}

// cleanJSON makes a display-only copy of decoded values, with each string
// cleaned and cut as the details show it, including map keys. The scripted
// host-show info object retains its original data and uses jsonSafe instead.
func cleanJSON(v any) any {
	switch v := v.(type) {
	case map[string]any:
		return cleanKeys(v)
	case []any:
		out := make([]any, len(v))
		for i, val := range v {
			out[i] = cleanJSON(val)
		}
		return out
	case string:
		return client.CleanDetail(v)
	}
	return v
}

// cleanKeys is a copy of m with its keys and values cleaned. Two keys that
// clean or cut to one string keep both entries (displayNames).
func cleanKeys(m map[string]any) map[string]any {
	return renamedDisplay(m, displayNames(slices.Collect(maps.Keys(m))))
}

// cleanInfo is what the host said about itself, cleaned as cleanJSON
// cleans it. Its tables of agents and of their versions share one name
// for each agent, so a version never shows under the name of another
// agent, also where two names clean to one.
func cleanInfo(info map[string]any) map[string]any {
	agents, _ := info["agents"].(map[string]any)
	versions, _ := info["agent_versions"].(map[string]any)
	names := displayNames(slices.Concat(slices.Collect(maps.Keys(agents)), slices.Collect(maps.Keys(versions))))
	out := cleanKeys(info)
	if agents != nil {
		out["agents"] = renamedDisplay(agents, names)
	}
	if versions != nil {
		out["agent_versions"] = renamedDisplay(versions, names)
	}
	return out
}

// displayNames gives each of keys its name in the output: the key cleaned
// and cut as the details show it. Two keys that clean or cut to one
// string keep two names, the same way on every run: in the order of the
// keys, each later one gets a suffix, " (2)", " (3)", within the bound of
// the details.
func displayNames(keys []string) map[string]string {
	slices.Sort(keys)
	keys = slices.Compact(keys)
	names := make(map[string]string, len(keys))
	taken := make(map[string]bool, len(keys))
	for _, k := range keys {
		base := client.CleanDetail(k)
		name := base
		for n := 2; taken[name]; n++ {
			suffix := fmt.Sprintf(" (%d)", n)
			name = client.CleanDetail(string([]rune(base)[:min(len([]rune(base)), client.DetailMax-len(suffix))])) + suffix
		}
		names[k], taken[name] = name, true
	}
	return names
}

// renamedDisplay is a copy of m with each key k as names[k], and each value
// cleaned.
func renamedDisplay(m map[string]any, names map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[names[k]] = cleanJSON(v)
	}
	return out
}
