package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// Addresses (docs/contributing/cli-spec.md 9.2). An address names a
// session in one of three forms: <host>/<session>, <session> alone, or -,
// the session last attached to. The session part is a session's ID or its
// name. A word of the form of an ID that the helper makes, 8 characters of
// api.SessionIDAlphabet with a digit, is always an ID and never a name: no
// name may have that form, so an address with it means one session. A bare word is
// always a session, also when a host has that name.
//
// The session cache only helps to find which host to ask. What a command
// acts on is decided by that host's own list, when the command dials it,
// from the word that the user typed: a session renamed since the cache saw
// it is not found, and a new session of that name is found.

// staleAfter is the age after which an address without a host asks a host
// for its list again, instead of using the session cache (question C-3).
const staleAfter = 60 * time.Second

// address is a session that a command line names, before the command
// dials its host.
type address struct {
	host client.Host
	// ref finds the session in the host's list, by its ID or its name: the
	// word that the user typed, or for "-" and a picked row, the ID.
	ref string
	// byID is true for "-" and a picked row: ref is a known ID, which finds
	// the session by its ID alone, never by a name, so that a session named
	// like that ID is never taken for it.
	byID bool
	// gone, with byID, is the error's message when the session is no longer
	// there, so that it names the session by its name, not by an ID that
	// the user did not type.
	gone string
}

// resolveAddress reads word, an address. An address without a host learns
// from the session cache which host to ask. strict is for a command that
// deletes: an address without a host then fails when a host did not
// answer, because that host may have a session of the same name.
func resolveAddress(cmd *cobra.Command, word string, strict bool) (address, error) {
	switch {
	case word == "-":
		return lastAddress()
	case word == "" || strings.Contains(word, "/"):
		hn, ref, err := client.SplitTarget(word)
		if err != nil {
			return address{}, err
		}
		h, err := client.FindHost(hn)
		if err != nil {
			return address{}, err
		}
		return address{host: h, ref: ref}, nil
	}
	return lookupBare(cmd, word, strict)
}

// lastAddress is the address "-": the session last attached to. Only a
// host that is no longer saved changes the error. Any other, such as a
// hosts.toml that does not decode, stays as it is.
func lastAddress() (address, error) {
	last, ok := client.LoadLast()
	if !ok {
		return address{}, &client.UserError{Msg: "no session has been attached yet", Hint: brand.Name + " ls", Code: "no_last_session", Kind: client.KindNotFound}
	}
	h, err := client.FindHost(last.Host)
	var unknown *client.UnknownNameError
	switch {
	case errors.As(err, &unknown):
		return address{}, &client.UserError{Msg: fmt.Sprintf("the last session was on %q, which is no longer a saved host", last.Host),
			Hint: brand.Name + " ls", Code: "host_not_found", Kind: client.KindNotFound}
	case err != nil:
		return address{}, err
	}
	name := last.Name
	if name == "" {
		name = "the session"
	}
	return address{host: h, ref: last.ID, byID: true, gone: fmt.Sprintf("the last session, %s on %s, is gone", shown(name), shown(h.Name))}, nil
}

// match is a session that a bare word names.
type match struct {
	host client.Host
	s    client.Session
	byID bool
}

// full is the match's address as a person reads it: host/ID.
func (m match) full() string { return m.host.Name + "/" + m.s.ID }

// lookupBare finds the host of a session named without one (cli-spec 9.2,
// question C-3). On each saved host the word matches a session's ID first,
// then its name. Matches on one host give the host to ask, with the word
// itself: that host's live list then finds the session, or finds two.
// Matches on more than one host are a usage error that lists them.
func lookupBare(cmd *cobra.Command, word string, strict bool) (address, error) {
	hosts, err := client.LoadHosts()
	if err != nil {
		return address{}, err
	}
	if len(hosts) == 0 {
		return address{}, &client.UserError{Msg: "no hosts yet", Hint: brand.Name + " host add <name>", Code: "no_hosts"}
	}
	l := cachedLists(hosts)
	l.ask(cmd.Context(), l.stale)
	matches := matchesOf(hosts, l.lists, word)
	if len(matches) == 0 {
		// A list that the cache has just seen may lack a session made since:
		// ask the hosts that it saved us asking.
		l.ask(cmd.Context(), l.notAsked(hosts))
		matches = matchesOf(hosts, l.lists, word)
	}
	oneHost := len(matches) > 0 && !slices.ContainsFunc(matches, func(m match) bool { return m.host.Name != matches[0].host.Name })
	if oneHost && strict && len(l.unanswered) > 0 {
		return address{}, unsureMatch(cmd, word, matches[0], l.unanswered)
	}
	if len(l.unanswered) > 0 {
		note(cmd, "hosts_unreached", fmt.Sprintf("%s did not answer, so %s as last seen.", joinAnd(l.unanswered), plural(len(l.unanswered), "its sessions are", "their sessions are")))
	}
	switch {
	case len(matches) == 0:
		return address{}, noSessionNamed(word, l.lists)
	case oneHost:
		return address{host: matches[0].host, ref: word}, nil
	}
	return address{}, ambiguousMatches(cmd, word, matches)
}

// hostLists is each saved host's sessions, for a lookup: from the session
// cache, and from the host itself when it was asked.
type hostLists struct {
	lists map[string][]client.Session
	// stale holds the hosts whose cached list is missing, older than
	// staleAfter, or from the future, as after the clock was set back.
	stale []client.Host
	// asked holds the hosts asked, and unanswered those that did not answer.
	asked      map[string]bool
	unanswered []string
}

// cachedLists is each host's list as the session cache has it.
func cachedLists(hosts []client.Host) *hostLists {
	cache := client.LoadSessionCache()
	l := &hostLists{lists: map[string][]client.Session{}, asked: map[string]bool{}}
	now := time.Now()
	for _, h := range hosts {
		l.lists[h.Name] = cache.Sessions[h.Name]
		if seen, ok := cache.Seen[h.Name]; !ok || seen.After(now) || now.Sub(seen) > staleAfter {
			l.stale = append(l.stale, h)
		}
	}
	return l
}

// ask asks hosts for their lists at once, without waking a stopped host,
// within the time limit of ls, and writes the answers to the cache. A host
// that does not answer keeps its cached list, whatever its age.
func (l *hostLists) ask(ctx context.Context, hosts []client.Host) {
	if len(hosts) == 0 {
		return
	}
	results := listAll(ctx, hosts)
	rememberLists(results)
	for _, r := range results {
		l.asked[r.Host] = true
		if r.err != nil {
			if !slices.Contains(l.unanswered, r.Host) {
				l.unanswered = append(l.unanswered, r.Host)
			}
			continue
		}
		l.lists[r.Host] = r.Sessions
	}
}

// notAsked is the hosts that l has not asked.
func (l *hostLists) notAsked(hosts []client.Host) []client.Host {
	var out []client.Host
	for _, h := range hosts {
		if !l.asked[h.Name] {
			out = append(out, h)
		}
	}
	return out
}

// matchesOf is the sessions that word names on hosts. On each host, the
// sessions with that ID win over those with that name, and a word that
// looks like an ID matches IDs alone. Then the hosts' matches add up, so
// an ID on one host and a name on another are two.
func matchesOf(hosts []client.Host, lists map[string][]client.Session, word string) []match {
	idOnly := api.LooksLikeID(word)
	var all []match
	for _, h := range hosts {
		var ids, names []match
		for _, s := range lists[h.Name] {
			switch {
			case word == s.ID:
				ids = append(ids, match{h, s, true})
			case word == s.Name && !idOnly:
				names = append(names, match{h, s, false})
			}
		}
		if len(ids) > 0 {
			all = append(all, ids...)
		} else {
			all = append(all, names...)
		}
	}
	return all
}

// noSessionNamed is the error of a bare word that names no session, with
// the sessions it may mean.
func noSessionNamed(word string, lists map[string][]client.Session) error {
	msg := fmt.Sprintf("no session named %q on any host", word)
	if api.LooksLikeID(word) {
		msg = fmt.Sprintf("no session has the ID %q on any host", word)
	}
	addrs, labels := suggestSessions(word, lists)
	return &usageError{
		UserError:   &client.UserError{Msg: msg, Hint: brand.Name + " ls", Code: "session_not_found", Kind: client.KindNotFound},
		suggestions: addrs, labels: labels,
	}
}

// suggestSessions returns at most three sessions of lists that word may
// mean, by a name or an ID within two edits: the address of each, host/ID,
// and its name, which the error shows beside it. A session whose name
// looks like an ID is found by its name here, and reached by its ID.
func suggestSessions(word string, lists map[string][]client.Session) (addrs, labels []string) {
	var things []named
	names := map[string]string{}
	for h, list := range lists {
		for _, s := range list {
			addr, ok := idAddress(h, s.ID)
			if !ok {
				continue
			}
			names[addr] = s.Name
			things = append(things, named{addr, []string{s.Name, s.ID}})
		}
	}
	addrs = suggest(word, things, false)
	for _, a := range addrs {
		labels = append(labels, names[a])
	}
	return addrs, labels
}

// ambiguousMatches is the error of a bare word that names more than one
// session. It lists each full address, and the Try: line holds the first.
func ambiguousMatches(cmd *cobra.Command, word string, matches []match) error {
	addrs := make([]string, len(matches))
	hosts := map[string]bool{}
	ids := 0
	for i, m := range matches {
		addrs[i] = m.full()
		hosts[m.host.Name] = true
		if m.byID {
			ids++
		}
	}
	what := "match"
	switch ids {
	case 0:
		what = "are named"
	case len(matches):
		what = "have the ID"
	}
	where := ""
	if len(hosts) == 1 {
		where = " on " + matches[0].host.Name
	}
	return &client.UserError{
		Msg:  fmt.Sprintf("%d sessions %s %q%s: %s", len(matches), what, word, where, strings.Join(addrs, ", ")),
		Hint: tryAddress(cmd, matches[0].host.Name, matches[0].s.ID), Code: "session_ambiguous", Kind: client.KindUsage,
	}
}

// unsureMatch is the error of a command that deletes, when the word names
// one session but a host did not answer. When that host is another, it may
// have a session of the same name. When it is the match's own host, its
// list may be out of date. The Try: line holds the session's host and ID,
// which mean that session alone.
func unsureMatch(cmd *cobra.Command, word string, m match, unanswered []string) error {
	var others []string
	own := false
	for _, h := range unanswered {
		if h == m.host.Name {
			own = true
		} else {
			others = append(others, h)
		}
	}
	that := "name"
	if api.LooksLikeID(word) {
		that = "ID"
	}
	var msg string
	switch {
	case own && len(others) == 0:
		msg = fmt.Sprintf("%q is on %s as last seen, but %s did not answer, so its list may be out of date", word, m.host.Name, m.host.Name)
	case own:
		msg = fmt.Sprintf("%q is on %s as last seen, but %s did not answer, so the list may be out of date, and another host may have a session of that %s",
			word, m.host.Name, joinAnd(unanswered), that)
	default:
		msg = fmt.Sprintf("%q is on %s, but %s did not answer, so %s may have a session of that %s too",
			word, m.host.Name, joinAnd(others), plural(len(others), "it", "they"), that)
	}
	return &client.UserError{Msg: msg, Hint: tryAddress(cmd, m.host.Name, m.s.ID), Code: "session_unsure", Kind: client.KindUsage}
}

// oldCommands holds the new form of each old command of cli-spec 3.2: its
// new name, and the flags that give the old meaning.
var oldCommands = map[string][]string{"kill": {"rm", "-y"}, "flip": {"attach"}}

// newForm is line, the command line of cmd, in the new form of an old
// command (cli-spec 3.2): kill becomes rm -y, and flip becomes attach. The
// flags go before any "--" that ends the flags. Any other line stays as it
// is.
func newForm(cmd *cobra.Command, line []string) []string {
	form, ok := oldCommands[cmd.Name()]
	if !ok || cmd.Parent() != cmd.Root() {
		return line
	}
	at := plain(cmd, line)
	if len(at) == 0 {
		return line
	}
	out := slices.Clone(line)
	out[at[0]] = form[0]
	return slices.Insert(out, flagsAt(cmd, out), form[1:]...)
}

// newPath is cmd's path without the product's name, in its new form when
// cmd is an old command: "rm -y" for kill.
func newPath(cmd *cobra.Command) string {
	if form, ok := oldCommands[cmd.Name()]; ok && cmd.Parent() == cmd.Root() {
		return strings.Join(form, " ")
	}
	return shortPath(cmd)
}

// found is the session that an address names, on its host.
type found struct {
	host client.Host
	conn *client.Conn
	s    *client.Session
}

// open dials the address's host, finds the session in the host's live
// list within 60 seconds, and runs fn with it and ctx. The live list, not
// the cache, decides which session the address names, and goes to the
// cache. The dial wakes a stopped host, as an attach does, unless ctx says
// not to (client.WithoutWake). A session that is gone is not found, with
// exit code 3. fn sets its own time limits: it may ask a question first.
func (a address) open(ctx context.Context, cmd *cobra.Command, fn func(context.Context, found) error) error {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	conn, err := client.Dial(cctx, a.host, true)
	if err != nil {
		return err
	}
	defer conn.Close()
	list, err := listSessions(cctx, conn)
	if err != nil {
		return err
	}
	client.RememberLists(map[string]client.HostList{a.host.Name: {Sessions: list, At: time.Now()}})
	s, err := a.find(cmd, list) //nolint:contextcheck // its Try: line parses a command line, which builds the command tree and runs nothing
	if err != nil {
		return err
	}
	return fn(ctx, found{host: a.host, conn: conn, s: s})
}

// find finds the address's session in list, the host's live list: a known
// ID by its ID alone, and a typed word by an ID, then a name, unless it
// looks like an ID.
func (a address) find(cmd *cobra.Command, list []client.Session) (*client.Session, error) {
	if !a.byID {
		return findIn(cmd, list, a.ref, a.host.Name)
	}
	s, err := resolveIDIn(list, a.ref, a.host.Name)
	if err == nil {
		return s, nil
	}
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "session_not_found" {
		return nil, err
	}
	msg := a.gone
	if msg == "" {
		msg = fmt.Sprintf("the session %s on %s is gone", a.ref, a.host.Name)
	}
	return nil, &client.UserError{Msg: msg, Hint: lsOf(a.host.Name), Code: "session_not_found", Kind: client.KindNotFound}
}

// byID is the session of list with the ID id, or nil. It never matches a
// name.
func byID(list []client.Session, id string) *client.Session {
	for i := range list {
		if list[i].ID == id {
			return &list[i]
		}
	}
	return nil
}

// withSession finds the session that word names, an address, dials its
// host, and runs fn with the session, within 60 seconds.
func withSession(cmd *cobra.Command, word string, fn func(context.Context, client.Host, *client.Conn, *client.Session) error) error {
	a, err := sessionArg(cmd, []string{word}, false)
	if err != nil {
		return err
	}
	return a.open(cmd.Context(), cmd, func(ctx context.Context, f found) error {
		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		return fn(ctx, f.host, f.conn, f.s)
	})
}

// findIn finds a session of host in list by its ID, or by its name when no
// session has that ID. A word that looks like an ID finds an ID alone,
// never a name. A name that two sessions share is a usage error, with the
// address of each by ID. A word that names none is not found, and the
// error repeats only that word, with the sessions that it may mean, by ID.
func findIn(cmd *cobra.Command, list []client.Session, ref, host string) (*client.Session, error) {
	idOnly := api.LooksLikeID(ref)
	var byName []client.Session
	for _, s := range list {
		if s.ID == ref {
			return &s, nil
		}
		if s.Name == ref && !idOnly {
			byName = append(byName, s)
		}
	}
	switch len(byName) {
	case 1:
		return &byName[0], nil
	case 0:
		msg := fmt.Sprintf("no session %q on %s", ref, host)
		if idOnly {
			msg = fmt.Sprintf("no session has the ID %q on %s", ref, host)
		}
		addrs, labels := suggestSessions(ref, map[string][]client.Session{host: list})
		return nil, &usageError{
			UserError:   &client.UserError{Msg: msg, Hint: lsOf(host), Code: "session_not_found", Kind: client.KindNotFound},
			suggestions: addrs, labels: labels,
		}
	}
	addrs := make([]string, len(byName))
	for i, s := range byName {
		addrs[i] = host + "/" + s.ID
	}
	return nil, &client.UserError{
		Msg:  fmt.Sprintf("%d sessions are named %q on %s: %s", len(byName), ref, host, strings.Join(addrs, ", ")),
		Hint: tryAddress(cmd, host, byName[0].ID), Code: "session_ambiguous", Kind: client.KindUsage,
	}
}

// joinAnd joins words as a sentence does: "a", "a and b", "a, b and c".
func joinAnd(words []string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

// plural is one when n is 1, and many otherwise.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
