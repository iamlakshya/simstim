package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

func (p *promptState) finishFolders() {
	if p.folderCancel != nil {
		p.folderCancel()
	}
	p.folderCtx, p.folderCancel = nil, nil
}

func (p *promptState) stopFolders() {
	p.folderSeq++
	p.finishFolders()
	p.folderProgram = nil
	p.asked, p.folders = "", nil
}

func quickFolderWord(p *promptState) string {
	text := p.in.value()
	return text[strings.LastIndex(text, " ")+1:]
}

func (m *Model) quickFoldersCurrent(p *promptState) bool {
	if p == nil || m.prompt != p || p.mode != promptNew || p.starting || p.folderProgram == nil || p.folderProgram.Err() != nil {
		return false
	}
	h := m.hostByName(p.host)
	return h != nil && h.online() && h.gen == p.folderGen && client.SameEntry(h.host, p.folderHost)
}

// askFolders is the quick-start completion path, owned by the current prompt.
func (m *Model) askFolders() tea.Cmd { return m.askQuickFolders(m.prompt) }

func (m *Model) askQuickFolders(p *promptState) tea.Cmd {
	if p == nil || m.prompt != p || p.mode != promptNew || p.starting || m.modal != nil || m.transferInputBlocked() {
		return nil
	}
	if p.folderProgram != nil && !m.quickFoldersCurrent(p) {
		p.stopFolders()
	}
	h := m.hostByName(p.host)
	word := quickFolderWord(p)
	if h == nil || !h.online() || !pathWord(word) || word == p.asked {
		return nil
	}
	p.stopFolders()
	p.folderProgram = m.backgroundContext()
	p.folderCtx, p.folderCancel = context.WithCancel(p.folderProgram)
	p.folderGen, p.folderHost, p.asked = h.gen, h.host, word
	ctx, seq, gen, originalHost := p.folderCtx, p.folderSeq, h.gen, h.host
	name, c := h.name(), h.conn
	return m.bgCmdFrom(ctx, func(ctx context.Context) tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if cctx.Err() != nil {
			return nil
		}
		res, err := c.Folders(cctx, word)
		if err != nil || cctx.Err() != nil {
			return nil
		}
		return foldersMsg{host: name, prefix: word, res: res, quick: true,
			quickOwner: p, quickSeq: seq, quickGen: gen, quickHost: originalHost}
	})
}

func (m *Model) acceptQuickFolders(msg foldersMsg, res client.FoldersResult) {
	p := m.prompt
	if !msg.quick || p == nil || p != msg.quickOwner || p.mode != promptNew || p.starting || p.host != msg.host ||
		p.asked != msg.prefix || quickFolderWord(p) != msg.prefix || p.folderSeq != msg.quickSeq ||
		p.folderCtx == nil || p.folderCtx.Err() != nil || !m.quickFoldersCurrent(p) || m.transferInputBlocked() {
		return
	}
	h := m.hostByName(msg.host)
	if h.gen != msg.quickGen || !client.SameEntry(h.host, msg.quickHost) {
		return
	}
	p.finishFolders()
	p.folders = res.Matches
}

// retireQuickFolders catches replacement and edits made through Update,
// including paste. A key that already began a new request keeps that request.
func (m *Model) retireQuickFolders(before *promptState, text string, seq uint64) {
	if before != nil && before != m.prompt {
		before.stopFolders()
	}
	p := m.prompt
	if p == nil {
		return
	}
	if p.starting || p.mode != promptNew || (p == before && p.folderSeq == seq && p.in.value() != text) ||
		(p.folderProgram != nil && !m.quickFoldersCurrent(p)) {
		p.stopFolders()
	}
}
