package cli

import (
	"fmt"
	"io"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// What a host sends is not trusted, and a command prints it cleaned
// (api.CleanText): a session's name, folder and reason, a process's name,
// a path, an error. Requests use the host's own values, and so does JSON
// output, where printJSON escapes what a terminal could act on. printError
// cleans every error that a command returns too, whatever built it, so an
// error that a site forgot to clean still prints clean.

// printPorts prints a session's ports. A process in the session names
// itself, so its name is the session's own text.
func printPorts(w io.Writer, ports []client.Port) {
	for _, p := range ports {
		fmt.Fprintf(w, "%-6d %-16s %s (pid %d)\n", p.Port, client.CleanLine(p.Address), client.CleanLine(p.Process), p.PID)
	}
}

// printRestore prints what a host restore did with each session, as
// RestoreResult.Text does, from a cleaned copy: every field is the host's
// text, the step to try too.
func printRestore(w io.Writer, res []api.RestoreResult) {
	for _, r := range res {
		r.ID, r.Name, r.Action = client.CleanLine(r.ID), client.CleanLine(r.Name), client.CleanLine(r.Action)
		r.Why, r.Hint = client.CleanLine(r.Why), client.CleanLine(r.Hint)
		fmt.Fprint(w, r.Text())
	}
}

// cleanAlertsInfo is a copy of a host's alerts file to print, cleaned. A
// token is never printed, only whether there is one.
func cleanAlertsInfo(info client.AlertsInfo) client.AlertsInfo {
	info.Path, info.Error, info.Hint = client.CleanPath(info.Path), client.CleanLine(info.Error), client.CleanLine(info.Hint)
	if info.Config == nil {
		return info
	}
	c := *info.Config
	c.Events = cleanList(c.Events)
	c.Delay, c.MinTurn = client.CleanLine(c.Delay), client.CleanLine(c.MinTurn)
	dests := make([]client.AlertDestination, len(c.Destinations))
	for i, d := range c.Destinations {
		d.Type, d.Events = client.CleanLine(d.Type), cleanList(d.Events)
		d.Run, d.URL, d.TokenFile = client.CleanLine(d.Run), client.CleanLine(d.URL), client.CleanPath(d.TokenFile)
		d.Priority, d.Format = client.CleanLine(d.Priority), client.CleanLine(d.Format)
		dests[i] = d
	}
	c.Destinations = dests
	info.Config = &c
	return info
}

// cleanList cleans each item of a list from a host, in a new list.
func cleanList(list []string) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = client.CleanLine(s)
	}
	return out
}

// jsonSafe escapes, in encoded JSON, each character that api.CleanText
// would take out of a value: C1 controls, DEL, bidirectional controls,
// zero-width and tag characters. encoding/json escapes C0 controls itself,
// and leaves these as they are. The decoded values do not change, but a
// terminal that prints the JSON gets nothing that it could act on. Every
// such character is inside a string, because JSON's own syntax is ASCII.
func jsonSafe(b []byte) []byte {
	var out []byte
	for i := 0; i < len(b); {
		r, n := utf8.DecodeRune(b[i:])
		if r < 0x7f || !api.Unclean(r) {
			if out != nil {
				out = append(out, b[i:i+n]...)
			}
			i += n
			continue
		}
		if out == nil {
			out = append(make([]byte, 0, len(b)+32), b[:i]...)
		}
		if r > 0xffff {
			hi, lo := utf16.EncodeRune(r)
			out = fmt.Appendf(out, `\u%04x\u%04x`, hi, lo)
		} else {
			out = fmt.Appendf(out, `\u%04x`, r)
		}
		i += n
	}
	if out == nil {
		return b
	}
	return out
}
