package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// resolveIDIn never treats a vanished known ID as a session name.
func resolveIDIn(list []client.Session, id, host string) (*client.Session, error) {
	if err := checkSessionID(host, id); err != nil {
		return nil, err
	}
	for _, s := range list {
		if s.ID == id {
			return &s, nil
		}
	}
	return nil, &client.UserError{Msg: fmt.Sprintf("no session %q on %s", id, shown(host)),
		Hint: brand.Name + " ls --host " + shellWord(host), Code: "session_not_found", Kind: client.KindNotFound}
}

func validSessionRows(list []client.Session) []client.Session {
	out := make([]client.Session, 0, len(list))
	for _, s := range list {
		if api.ValidSessionID(s.ID) {
			out = append(out, s)
		}
	}
	return out
}

// idArgument checks explicit legacy IDs before host lookup or connection.
// A dash here is a literal target, never the last-session shorthand.
func idArgument(cmd *cobra.Command, args []string) (string, string, error) {
	if len(args) == 0 {
		return "", "", missingArgument(cmd, "a session ID, as <host>/<id>")
	}
	host, id, err := client.SplitTarget(args[0])
	if err != nil {
		return "", "", err
	}
	if err := checkSessionID(host, id); err != nil {
		var ue *client.UserError
		if errors.As(err, &ue) {
			ue.Kind = client.KindUsage
		}
		return "", "", err
	}
	return host, id, nil
}

// sessionArg is the address that the command line names, or, with none,
// the session that the user picks (cli-spec 6.3).
func sessionArg(cmd *cobra.Command, args []string, strict bool) (address, error) {
	if boolFlag(cmd, "id") {
		host, ref, err := idArgument(cmd, args)
		if err != nil {
			return address{}, err
		}
		h, err := client.FindHost(host)
		return address{host: h, ref: ref, byID: true, gone: fmt.Sprintf("no session %q on %s", ref, shown(h.Name))}, err
	}
	if len(args) > 0 {
		return resolveAddress(cmd, args[0], strict)
	}
	return pickSession(cmd, "<host>/<session>")
}
