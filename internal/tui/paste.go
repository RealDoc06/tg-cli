package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/RealDoc06/tg-cli/internal/clipboard"
)

// The terminal's clipboard. In kitty, pastes read the clipboard through the
// terminal (OSC 5522), which is the user's own clipboard even over SSH, and
// so does ctrl+v where the system's clipboard tools cannot reach one.
// Elsewhere the terminal pastes text and ctrl+v only has the tools, see
// pasteImage.

// termReply collects one OSC 5522 reply, from its OK packet to its DONE. id
// is the attachSeq of the read it answers, 0 for a paste event.
type termReply struct {
	id   int
	pw   string
	data map[string][]byte
}

// termPacket handles one packet of a reply from the terminal's clipboard.
// Replies to reads that were cancelled or replaced are dropped.
func (m *model) termPacket(p clipboard.Packet) tea.Cmd {
	switch p.Status {
	case "OK":
		m.reply = &termReply{id: p.ID, pw: p.PW, data: map[string][]byte{}}
	case "DATA":
		if m.reply != nil {
			m.reply.data[p.MIME] = append(m.reply.data[p.MIME], p.Data...)
		}
	case "DONE":
		r := m.reply
		m.reply = nil
		if r == nil {
			return nil
		}
		if list, ok := r.data[clipboard.ListTypes]; ok {
			return m.termListed(r, list)
		}
		if r.id != m.attachSeq {
			return nil
		}
		text := clipboard.Text(r.data)
		if m.focus != focusInput {
			m.pasting = 0
			if text == "" {
				return nil
			}
			return m.update(tea.PasteMsg{Content: text}) // into the chat filter
		}
		msg := clipboardMsg{seq: r.id, png: r.data["image/png"], text: text}
		if msg.png == nil && text == "" {
			msg.err = clipboard.ErrNoImage
		}
		return m.update(msg)
	default: // an error code, like EPERM when the user said no
		m.reply = nil
		if p.ID == m.attachSeq {
			m.pasting = 0
			m.status = fmt.Sprintf("the terminal did not share its clipboard (%s)", p.Status)
		}
	}
	return nil
}

// termListed reads what the clipboard holds once the terminal listed its
// types, after a paste event or ctrl+v.
func (m *model) termListed(r *termReply, list []byte) tea.Cmd {
	if r.id != 0 && r.id != m.attachSeq {
		return nil // ctrl+v was cancelled or replaced
	}
	mime := clipboard.Pick(list, m.focus == focusInput)
	if mime == "" {
		if r.id != 0 { // ctrl+v on an empty clipboard
			return m.update(clipboardMsg{seq: r.id, err: clipboard.ErrNoImage})
		}
		return nil
	}
	pw, name := r.pw, "Paste event"
	if pw == "" {
		pw, name = m.termPW, "tg"
	}
	if r.id == 0 {
		m.startRead()
	}
	return tea.Raw(clipboard.ReadRequest(m.attachSeq, pw, name, mime))
}

// startRead begins a clipboard read for the composer. Only the latest read
// may land, and enter waits for it.
func (m *model) startRead() {
	m.attachSeq++
	m.pasting = m.attachSeq
}
