package tui

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// connStatus is where a host's background connection stands.
type connStatus int

const (
	stConnecting connStatus = iota
	stOnline
	stOffline // unreachable; retrying with backoff
	stFailed  // reachable, but something needs fixing; retrying slowly
)

// hostConn is one saved host and its background connection. Only Update
// touches it; the work happens in tea.Cmds that report back with messages.
type hostConn struct {
	host      client.Host
	tier      client.Tier
	tierUnset bool
	idChanged bool
	status    connStatus
	conn      Conn
	gen       int // bumped on every dial, so late answers from old connections are dropped
	err       error
	sessions  []client.Session
	loaded    bool
	// seenAt is when the host's list last arrived, for the session cache,
	// as the worker that fetched it saw it: a list that the launcher saves
	// later keeps this time.
	seenAt     time.Time
	rtts       rttWindow // the round trips of the last minute (rtt.go)
	rtt        rttSpread // their spread, as the launcher shows it
	info       *client.HostInfo
	backoff    time.Duration
	listing    bool
	pinging    bool
	doctoring  bool
	needRetire bool
	dialCancel context.CancelFunc
	dialSerial chan struct{}
	// changes counts local edits to the list (a new, renamed or deleted
	// session). A list asked for before the latest edit is stale.
	changes int
}

func (h *hostConn) name() string { return h.host.Name }

func (h *hostConn) online() bool { return h.status == stOnline && h.conn != nil }

// Messages from the connection commands.
type (
	dialledMsg struct {
		name      string
		gen       int
		conn      Conn
		err       error
		reconnect bool
	}
	connDoneMsg struct {
		name string
		gen  int
	}
	redialMsg struct {
		name string
		gen  int
	}
	sessionsMsg struct {
		name    string
		gen     int
		changes int
		list    []client.Session
		err     error
		// at is when the list arrived, taken by the worker that asked: the
		// launcher may handle the message much later, as after an attach.
		at time.Time
	}
	latencyMsg struct {
		name string
		gen  int
		d    time.Duration
	}
	doctorMsg struct {
		name string
		gen  int
		info client.HostInfo
		err  error
	}
	refreshTickMsg struct{}
	latencyTickMsg struct{}
)

// loadSavedHosts reads the saved hosts and their tiers for New.
func loadSavedHosts(ctx context.Context, be Backend) ([]*hostConn, error) {
	hosts, err := be.LoadHosts(ctx)
	if err != nil {
		return nil, err
	}
	tiers, err := be.LoadTiers(ctx, hosts)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(hosts, func(i, j int) bool { return hosts[i].Name < hosts[j].Name })
	out := make([]*hostConn, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, &hostConn{host: h, tier: tiers.Of(h.Name), tierUnset: tiers.NeedsAnswer(h.Name), idChanged: tiers.IDChanged(h.Name)})
	}
	return out, nil
}

func (m *Model) hostByName(name string) *hostConn {
	for _, h := range m.hosts {
		if h.name() == name {
			return h
		}
	}
	return nil
}

// dial starts a background connection to h.
func (m *Model) dial(h *hostConn) tea.Cmd {
	return m.dialWith(h, nil, false)
}

func (m *Model) dialWith(h *hostConn, drop Conn, manual bool) tea.Cmd {
	if h.dialCancel != nil {
		h.dialCancel()
	}
	ctx, cancel := context.WithCancel(m.backgroundContext())
	h.dialCancel = cancel
	if h.dialSerial == nil {
		h.dialSerial = make(chan struct{}, 1)
	}
	h.gen++
	h.status = stConnecting
	h.listing, h.pinging, h.doctoring = false, false, false
	h.rtts, h.rtt = nil, rttSpread{}
	gen, host, retire, be, set, serial := h.gen, h.host, h.needRetire, m.be, m.conns, h.dialSerial
	return m.bgCmdFrom(ctx, func(bgCtx context.Context) tea.Msg {
		// The set retains drop until this command starts or Close retires it.
		if drop != nil {
			set.drop(drop)
		}
		select {
		case serial <- struct{}{}:
			defer func() { <-serial }()
		case <-bgCtx.Done():
			return nil
		}
		if bgCtx.Err() != nil {
			return nil
		}
		dctx, finish := context.WithTimeout(bgCtx, 30*time.Second)
		defer finish()
		var c Conn
		var err error
		if retire {
			c, err = be.Reconnect(dctx, host)
			if err == nil && c != nil {
				c = cleanConn{c}
			}
		} else {
			c, err = dialClean(dctx, be, host)
		}
		if err == nil && c != nil {
			if bgCtx.Err() != nil || !set.addUnadopted(c) {
				c.Close()
				return nil
			}
		}
		return dialledMsg{name: host.Name, gen: gen, conn: c, err: err, reconnect: manual}
	})
}

func (m *Model) waitDone(name string, gen int, c Conn) tea.Cmd {
	return m.bgCmd(func(bgCtx context.Context) tea.Msg {
		m.waiters.Add(1)
		defer m.waiters.Add(-1)
		select {
		case <-c.Done():
			return connDoneMsg{name: name, gen: gen}
		case <-bgCtx.Done():
			return nil
		}
	})
}

func (m *Model) dropConn(c Conn) tea.Cmd {
	if c == nil {
		return nil
	}
	set := m.conns
	return func() tea.Msg {
		set.drop(c)
		return nil
	}
}

// listSessions asks h for its sessions.
func (m *Model) listSessions(h *hostConn) tea.Cmd {
	if !h.online() || h.listing {
		return nil
	}
	h.listing = true
	name, gen, changes, c := h.name(), h.gen, h.changes, h.conn
	return m.bgCmd(func(bgCtx context.Context) tea.Msg {
		cctx, cancel := context.WithTimeout(bgCtx, 15*time.Second)
		defer cancel()
		list, err := c.Sessions(cctx)
		return sessionsMsg{name: name, gen: gen, changes: changes, list: list, err: err, at: time.Now()}
	})
}

// ping times one round trip to h, unless one is on its way.
func (m *Model) ping(h *hostConn) tea.Cmd {
	if !h.online() || h.pinging {
		return nil
	}
	h.pinging = true
	name, gen, c := h.name(), h.gen, h.conn
	return m.bgCmd(func(bgCtx context.Context) tea.Msg {
		cctx, cancel := context.WithTimeout(bgCtx, 15*time.Second)
		defer cancel()
		d, err := c.RoundTrip(cctx)
		if err != nil {
			d = 0
		}
		return latencyMsg{name: name, gen: gen, d: d}
	})
}

func (m *Model) doctor(h *hostConn) tea.Cmd {
	if !h.online() || h.doctoring {
		return nil
	}
	h.doctoring = true
	name, gen, c := h.name(), h.gen, h.conn
	return m.bgCmd(func(bgCtx context.Context) tea.Msg {
		cctx, cancel := context.WithTimeout(bgCtx, 30*time.Second)
		defer cancel()
		info, err := c.Doctor(cctx)
		return doctorMsg{name: name, gen: gen, info: info, err: err}
	})
}

// nextBackoff is 1 s, then doubles up to 30 s.
func nextBackoff(d time.Duration) time.Duration {
	if d <= 0 {
		return time.Second
	}
	return min(d*2, maxBackoff)
}

// offline reports whether err means the host could not be reached at all,
// as opposed to reached but not ready.
func offline(err error) bool {
	if ue, ok := asUserError(err); ok && ue.Code == "reconnect_failed" {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, client.ErrClosed) {
		return true
	}
	// The whole message, not the text that the launcher draws, which a
	// long error has cut.
	msg := err.Error()
	return strings.HasPrefix(msg, "cannot reach") || strings.Contains(msg, "timed out") ||
		strings.Contains(msg, "Connection refused") || strings.Contains(msg, "No route to host")
}

// reconnect closes h's bridge and shared SSH connection, if any, and dials a
// fresh connection now.
func (m *Model) reconnect(h *hostConn) tea.Cmd {
	if h == nil {
		return nil
	}
	old := h.conn
	h.conn = nil
	h.info = nil
	h.needRetire = true
	h.backoff = 0
	return m.dialWith(h, old, true)
}

// addHost puts a new or changed host in the list. If conn is set, it is a
// ready connection to adopt, so the host needs no second dial.
func (m *Model) addTierHost(host client.Host, tier client.Tier, idChanged bool, conn Conn, info *client.HostInfo) tea.Cmd {
	h := m.hostByName(host.Name)
	if h != nil && h.dialCancel != nil {
		h.dialCancel()
	}
	var cmds []tea.Cmd
	if h == nil {
		h = &hostConn{host: host}
		m.hosts = append(m.hosts, h)
		sort.SliceStable(m.hosts, func(i, j int) bool { return m.hosts[i].name() < m.hosts[j].name() })
	} else {
		if !sameRoute(h.host, host) {
			h.sessions, h.info, h.err = nil, nil, nil
			h.loaded, h.listing, h.pinging = false, false, false
			h.backoff, h.rtts, h.rtt = 0, nil, rttSpread{}
			m.syncSel()
		}
		h.host = host
		if h.conn != nil {
			cmds = append(cmds, m.dropConn(h.conn))
			h.conn = nil
		}
	}
	if !tier.Valid() {
		tier = client.TierShared
	}
	h.tier, h.idChanged = tier, idChanged
	if conn != nil && !m.conns.adopt(conn) {
		conn = nil
	}
	h.needRetire = false
	if conn == nil {
		return tea.Batch(append(cmds, m.dial(h))...)
	}
	if conn.HostIDChanged() {
		h.tier, h.idChanged = client.TierShared, true
		cmds = append(cmds, m.toastHostIDChanged(h.name()))
	}
	h.gen++
	h.conn, h.status, h.err, h.backoff, h.info = conn, stOnline, nil, 0, info
	h.listing, h.pinging, h.doctoring = false, false, false
	h.rtts, h.rtt = nil, rttSpread{} // the host may be reached another way now
	cmds = append(cmds, m.listSessions(h), m.ping(h), m.waitDone(h.name(), h.gen, conn))
	if info == nil {
		cmds = append(cmds, m.doctor(h))
	}
	return tea.Batch(cmds...)
}

// Session order and looks.

// rank orders sessions: needs input, then working, idle, exited.
func rank(s client.Session) int {
	switch s.State {
	case api.StateNeedsInput:
		return 0
	case api.StateWorking, api.StateStarting:
		return 1
	case api.StateIdle:
		return 2
	default:
		return 3
	}
}

func sortSessions(list []client.Session) {
	sort.SliceStable(list, func(i, j int) bool {
		ri, rj := rank(list[i]), rank(list[j])
		if ri != rj {
			return ri < rj
		}
		if !list[i].CreatedAt.Equal(list[j].CreatedAt) {
			return list[i].CreatedAt.After(list[j].CreatedAt)
		}
		return list[i].ID < list[j].ID
	})
}

// look is how a session's state is drawn: its glyph and its label. A
// working session's glyph is alive, and one that needs the user breathes.
// An ended session's look says how its agent ended (endLook).
func (m *Model) look(s client.Session) (glyph, label seg) {
	if ended(s) {
		return endLook(s)
	}
	switch s.State {
	case api.StateNeedsInput:
		return seg{glyphNeedsInput, m.breath(sWarning)}, seg{"needs input", sWarning}
	case api.StateWorking:
		return seg{m.working(s.ID), sKey}, seg{"working", sKey}
	case api.StateStarting:
		return seg{m.spinner(), sKey}, seg{"starting", sFaint}
	case api.StateIdle:
		return seg{glyphIdle, sFaint}, seg{"idle", sFaint}
	}
	return seg{glyphIdle, sFaint}, seg{strings.ReplaceAll(shown(s.State), "_", " "), sFaint}
}

// traceStyle colours a session's trace like its state: cyan for working,
// yellow for needs input, faint otherwise.
func traceStyle(s client.Session) lipgloss.Style {
	switch s.State {
	case api.StateWorking:
		return sKey
	case api.StateNeedsInput:
		return sWarning
	default:
		return sFaint
	}
}

// homePath shows a path on h with its home folder as "~".
func homePath(h *hostConn, p string) string {
	if h == nil || h.info == nil || h.info.User == "" {
		return p
	}
	home := "/home/" + h.info.User
	if h.info.User == "root" {
		home = "/root"
	}
	if p == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(p, home+"/"); ok {
		return "~/" + rest
	}
	return p
}

// since is when the session entered its state, for the age column.
func since(s client.Session) time.Time {
	if !s.Since.IsZero() {
		return s.Since
	}
	return s.CreatedAt
}

// alertNew raises a desktop notification (OSC 777) when a session starts to
// need the user while the terminal is in the background. The launcher draws
// the same news in its list; this is for when nobody is looking.
func (m *Model) alertNew(h *hostConn, list []client.Session) tea.Cmd {
	if m.focused || !h.loaded {
		return nil
	}
	before := map[string]string{}
	for _, s := range h.sessions {
		before[s.ID] = s.State
	}
	var b strings.Builder
	for _, s := range list {
		if s.State != api.StateNeedsInput || before[s.ID] == api.StateNeedsInput {
			continue
		}
		body := shown(s.Name) + " needs you"
		if r := shown(s.Reason); r != "" {
			body += ": " + r
		}
		b.WriteString("\x1b]777;notify;" + oscText(brand.DisplayName+" · "+shown(h.name())) + ";" + oscText(body) + "\x1b\\")
	}
	if b.Len() == 0 {
		return nil
	}
	return tea.Raw(b.String())
}

// rememberCreatedHost lists a machine that Create already saved when a stop
// raced its completion. It issues no dial or preparation request after the stop.
func (m *Model) rememberCreatedHost(host client.Host) {
	if m.hostByName(host.Name) != nil {
		return
	}
	m.hosts = append(m.hosts, &hostConn{host: host, status: stOffline})
	sort.SliceStable(m.hosts, func(i, j int) bool { return m.hosts[i].name() < m.hosts[j].name() })
	m.syncSel()
}

// oscText keeps text safe inside an OSC string.
func oscText(s string) string {
	return strings.ReplaceAll(shown(s), ";", " ")
}

// addHost retains the package-local call shape used by lifecycle fixtures.
// Policy-bearing production saves use addTierHost with their committed tier.
func (m *Model) addHost(host client.Host, conn Conn, info *client.HostInfo) tea.Cmd {
	tier, changed := client.TierUnknown, false
	if h := m.hostByName(host.Name); h != nil {
		tier, changed = h.tier, h.idChanged
	}
	return m.addTierHost(host, tier, changed, conn, info)
}
