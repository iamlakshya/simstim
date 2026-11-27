package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

func (d *newSession) finishFolders() {
	if d.folderCancel != nil {
		d.folderCancel()
	}
	d.folderCtx, d.folderCancel = nil, nil
}

func (d *newSession) stopFolders() {
	d.folderSeq++
	d.finishFolders()
}

// fetchFolders binds a completion to the actual form and host connection.
func (m *Model) fetchFolders(d *newSession) tea.Cmd {
	if m.modal != d || d.starting || m.transferInputBlocked() {
		return nil
	}
	h := m.hostByName(d.host)
	if h == nil || !h.online() {
		return nil
	}
	d.stopFolders()
	d.folderCtx, d.folderCancel = context.WithCancel(m.backgroundContext())
	d.folderGen, d.folderHost = h.gen, h.host
	ctx, seq, gen, originalHost := d.folderCtx, d.folderSeq, h.gen, h.host
	prefix, name, c := d.folder.value(), h.name(), h.conn
	return m.bgCmdFrom(ctx, func(ctx context.Context) tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if cctx.Err() != nil {
			return nil
		}
		res, err := c.Folders(cctx, prefix)
		if err != nil || cctx.Err() != nil {
			return nil
		}
		return foldersMsg{host: name, prefix: prefix, res: res, owner: d, seq: seq, gen: gen, originalHost: originalHost}
	})
}

func (m *Model) retireFormFolders(before modal) {
	if d, ok := questionOwner(before).(*downgradeSheet); ok && questionOwner(m.modal) != d {
		d.closed = true
	}
	old, _ := questionOwner(before).(*newSession)
	current, _ := questionOwner(m.modal).(*newSession)
	if old != nil && (old != current || old.starting) {
		old.stopFolders()
	}
	if current != nil && current.folderCtx != nil {
		h := m.hostByName(current.host)
		if h == nil || !h.online() || h.gen != current.folderGen || !client.SameEntry(h.host, current.folderHost) {
			current.stopFolders()
		}
	}
}

// onFolders takes folder completions, for the quick start or the form.
func (m *Model) onFolders(msg foldersMsg) tea.Cmd {
	res := cleanFolders(msg.res)
	m.acceptQuickFolders(msg, res)
	if d, ok := m.modal.(*newSession); ok && !msg.quick && d == msg.owner && d.host == msg.host && !d.starting &&
		d.folderSeq == msg.seq && d.folderCtx != nil && d.folderCtx.Err() == nil && !m.transferInputBlocked() {
		h := m.hostByName(msg.host)
		if h != nil && h.online() && h.gen == msg.gen && client.SameEntry(h.host, msg.originalHost) {
			d.finishFolders()
			d.setSuggestions(msg.prefix, res)
		}
	}
	return nil
}
