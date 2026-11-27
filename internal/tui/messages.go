package tui

import "github.com/iamlakshya/simstim/internal/client"

// Timer and result messages.
type (
	previewTickMsg struct{ seq int }
	opDoneMsg      struct {
		host string
		ok   string
		err  error
		drop string // a session to take off the list
		// warn, when set, replaces ok: the operation worked, with a
		// problem that hint says how to fix.
		warn, hint string
	}
	foldersMsg struct {
		quickOwner   *promptState
		quickSeq     uint64
		quickGen     int
		quickHost    client.Host
		owner        *newSession
		seq          uint64
		gen          int
		originalHost client.Host
		host, prefix string
		res          client.FoldersResult
		quick        bool // for the quick-start line
	}
	activityTickMsg struct{}
	startedMsg      struct {
		startReq   // what started: a question about it sends it again
		s          *client.Session
		err        error
		cleanupErr error
		warn       *client.UserError // the answers about setup scripts did not parse
		settingsRead
	}
	attachDoneMsg struct {
		host, id, name string
		res            client.AttachResult
		err            error
		settingsRead
	}
	sshHostsMsg struct{ hosts []string }
)
