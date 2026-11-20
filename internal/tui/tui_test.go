package tui

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/RealDoc06/tg-cli/internal/core"
	"github.com/RealDoc06/tg-cli/internal/demo"
)

// loaded returns a model with the demo chats listed, at the given size.
func loaded(t *testing.T, width, height int) *model {
	t.Helper()
	be := demo.New()
	m := newModel(context.Background(), be, "")
	m.version = "test"
	chats, err := be.Chats(m.ctx, core.ChatQuery{})
	if err != nil {
		t.Fatal(err)
	}
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m.Update(chatsMsg{chats: chats})
	return m
}

// open selects the chat at index i and loads its history synchronously.
func open(t *testing.T, m *model, i int) {
	t.Helper()
	m.sel = i
	m.openSelected()
	msgs, err := m.be.History(m.ctx, m.openID, core.HistoryQuery{Limit: historyPage})
	if err != nil {
		t.Fatal(err)
	}
	m.Update(historyMsg{chatID: m.openID, msgs: msgs})
}

// Every rendered row must be exactly as wide as the terminal: a longer one
// wraps and corrupts the screen, a shorter one leaves stale cells behind.
func TestViewFillsTheTerminal(t *testing.T) {
	for _, size := range [][2]int{{120, 30}, {80, 24}, {72, 10}, {50, 20}, {20, 6}} {
		m := loaded(t, size[0], size[1])
		for _, state := range []string{"list", "opened", "replying with a menu open"} {
			switch state {
			case "opened":
				open(t, m, 1)
			case "replying with a menu open":
				m.pickReply(-1)
				m.openMenu(m.width-1, m.height-5) // the newest message
				if m.menu == nil && size[1] >= 10 {
					t.Errorf("%v: no menu opened on the newest message", size)
				}
			}
			rows := strings.Split(m.View().Content, "\n")
			if len(rows) != size[1] {
				t.Errorf("%v %s: %d rows", size, state, len(rows))
			}
			for i, row := range rows {
				if w := ansi.StringWidth(row); w != size[0] {
					t.Errorf("%v %s: row %d is %d cells: %q", size, state, i, w, ansi.Strip(row))
				}
			}
		}
	}
}

func TestIncomingMessageBumpsChat(t *testing.T) {
	m := loaded(t, 120, 30)
	pinned := m.chats[0]
	if !pinned.Pinned {
		t.Fatal("the demo data is expected to start with a pinned chat")
	}
	target := m.chats[4]
	m.sel = 2
	selected := m.chats[2].ID

	m.Update(eventMsg(core.Event{Kind: core.EventMessage, Chat: target, Message: core.Message{
		ID: 9999, ChatID: target.ID, From: target.Title, Date: time.Now(), Text: "ping",
	}}))

	if m.chats[0].ID != pinned.ID || m.chats[1].ID != target.ID {
		t.Fatalf("order is %q, %q: want the pinned chat, then the one with the new message", m.chats[0].Title, m.chats[1].Title)
	}
	if got := m.chats[1]; got.Unread != target.Unread+1 || got.Last.Text != "ping" {
		t.Errorf("chat after the event: unread %d, last %q", got.Unread, got.Last.Text)
	}
	if m.visible()[m.sel].ID != selected {
		t.Error("the cursor moved to another chat")
	}
}

// A message arriving twice (the send result, then the live update) must not
// show twice, and reading a chat must not count its messages as unread.
func TestOpenChatDedupesAndStaysRead(t *testing.T) {
	m := loaded(t, 120, 30)
	open(t, m, 1)
	chat := *m.chat(m.openID)
	before := len(m.histories[chat.ID].msgs)

	msg := core.Message{ID: 9999, ChatID: chat.ID, From: "Someone", Date: time.Now(), Text: "hello"}
	m.Update(sentMsg{msg: msg})
	m.Update(eventMsg(core.Event{Kind: core.EventMessage, Chat: chat, Message: msg}))

	if got := len(m.histories[chat.ID].msgs); got != before+1 {
		t.Errorf("history grew by %d messages, want 1", got-before)
	}
	// An edit of a message older than the loaded page stays out of it.
	old := core.Message{ID: 1, ChatID: chat.ID, From: "Someone", Date: time.Now(), Text: "old, edited", Edited: true}
	m.Update(eventMsg(core.Event{Kind: core.EventEdit, Chat: chat, Message: old}))
	if last := m.histories[chat.ID].msgs; last[len(last)-1].ID != msg.ID {
		t.Error("an edit of an unloaded older message was shown as the newest")
	}
	// A new message arriving out of order is still shown.
	late := core.Message{ID: 9000, ChatID: chat.ID, From: "Someone", Date: time.Now(), Text: "late"}
	m.Update(eventMsg(core.Event{Kind: core.EventMessage, Chat: chat, Message: late}))
	msgs := m.histories[chat.ID].msgs
	if !slices.ContainsFunc(msgs, func(o core.Message) bool { return o.ID == late.ID }) {
		t.Error("a new message that arrived out of order was dropped")
	}
	if !slices.IsSortedFunc(msgs, func(a, b core.Message) int { return cmp.Compare(a.ID, b.ID) }) {
		t.Error("the history is not in message order")
	}
	// One older than the loaded page waits for the older pages, which load
	// from the oldest message shown and would skip what lies between.
	oldest := msgs[0].ID
	m.histories[chat.ID].done = false // the server has older pages
	m.Update(eventMsg(core.Event{Kind: core.EventMessage, Chat: chat, Message: core.Message{ID: oldest - 1, ChatID: chat.ID, Text: "older"}}))
	if m.histories[chat.ID].msgs[0].ID != oldest {
		t.Error("a message older than the loaded page was added to it")
	}
	if unread := m.chat(chat.ID).Unread; unread != 0 {
		t.Errorf("open chat has %d unread", unread)
	}
}

func TestFilterNarrowsChats(t *testing.T) {
	m := loaded(t, 120, 30)
	m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	for _, r := range "bob" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if chats := m.visible(); len(chats) != 1 || chats[0].Title != "Bob Chen" {
		t.Fatalf("filter left %d chats", len(chats))
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if len(m.visible()) != len(m.chats) || m.focus != focusChats {
		t.Error("escape did not clear the filter")
	}
}

// A refusal from the guardrails is a long sentence. It must be shown, not
// dropped for not fitting next to the key hints.
func TestLongErrorIsShown(t *testing.T) {
	m := loaded(t, 80, 24)
	m.Update(errMsg{errors.New(`account "work" is read-only, sending is off. Only the account's owner can change that`)})
	rows := strings.Split(m.View().Content, "\n")
	if last := ansi.Strip(rows[len(rows)-1]); !strings.Contains(last, `account "work" is read-only`) {
		t.Errorf("status line: %q", last)
	}
}

// screenRow is the row the text is drawn on, -1 when it is not on screen.
func screenRow(m *model, text string) int {
	return slices.IndexFunc(strings.Split(m.View().Content, "\n"), func(row string) bool {
		return strings.Contains(ansi.Strip(row), text)
	})
}

// A right click must act on the message under the pointer, and an edit must
// reach the backend and show at once.
func TestRightClickEditsTheClickedMessage(t *testing.T) {
	m := loaded(t, 120, 30)
	open(t, m, 1)
	const original = "I'll move the media VM back tonight"
	y := screenRow(m, original)
	m.Update(tea.MouseClickMsg{X: 90, Y: y, Button: tea.MouseRight})
	if m.menu == nil || m.menu.msg.Text != original {
		t.Fatalf("menu: %+v", m.menu)
	}
	i := slices.IndexFunc(m.menu.items, func(item menuItem) bool { return item.label == "Edit" })
	m.Update(tea.MouseClickMsg{X: m.menu.x, Y: m.menu.y + i, Button: tea.MouseLeft})
	if m.menu != nil || m.input.Value() != original {
		t.Fatalf("after picking Edit: menu %v, composer %q", m.menu, m.input.Value())
	}

	m.input.SetValue("moving it at 9pm")
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.Update(cmd())
	if y := screenRow(m, "moving it at 9pm (edited)"); y < 0 {
		t.Error("the edit is not on screen")
	}
	saved, _ := m.be.History(m.ctx, m.openID, core.HistoryQuery{Limit: historyPage})
	if !slices.ContainsFunc(saved, func(msg core.Message) bool { return msg.Text == "moving it at 9pm" && msg.Edited }) {
		t.Error("the backend did not get the edit")
	}
}

// ctrl+up and ctrl+down walk the reply through the messages, as in Telegram,
// and the message sent answers the one picked.
func TestCtrlArrowsPickTheReply(t *testing.T) {
	m := loaded(t, 120, 30)
	open(t, m, 1)
	msgs := m.histories[m.openID].msgs
	up := tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModCtrl}
	down := tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl}

	m.Update(up)
	m.Update(up)
	if want := msgs[len(msgs)-2].ID; m.replyTo != want {
		t.Fatalf("after two ctrl+up the reply is to %d, want %d", m.replyTo, want)
	}
	m.Update(down)
	m.Update(down)
	if m.replyTo != 0 {
		t.Fatalf("going past the newest message kept the reply to %d", m.replyTo)
	}

	m.Update(up)
	for _, r := range "on it" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	sent := cmd().(sentMsg)
	if want := msgs[len(msgs)-1].ID; sent.msg.ReplyTo != want || m.replyTo != 0 {
		t.Errorf("sent a reply to %d, want %d; reply left set to %d", sent.msg.ReplyTo, want, m.replyTo)
	}
}

// The composer is one line. Editing must not join a message's lines, and an
// edit must give back the draft it replaced.
func TestEditKeepsLinesAndDraft(t *testing.T) {
	m := loaded(t, 120, 30)
	open(t, m, 1)
	h := m.histories[m.openID]
	for _, text := range []string{"line one\nline two", "decode failed: \uFFFD"} {
		h.msgs = append(h.msgs, core.Message{ID: 9999, ChatID: m.openID, Out: true, Date: time.Now(), Text: text})
		m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
		if m.editing != 0 || m.status == "" {
			t.Fatalf("%q, which the composer would change, was opened for editing", text)
		}
		h.msgs = h.msgs[:len(h.msgs)-1]
	}

	m.input.SetValue("half typed")
	msg, _ := m.lastOwn()
	m.edit(msg)
	if m.input.Value() != msg.Text {
		t.Fatalf("composer holds %q", m.input.Value())
	}
	if _, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Error("an unchanged edit was sent")
	}
	if m.input.Value() != "half typed" {
		t.Errorf("the draft became %q", m.input.Value())
	}
}

// Saving an edit puts the draft and its reply back at once, and nothing is
// sent until the save is done. A failed edit comes back over the draft, and
// cancelling it gives the draft back again.
func TestEditGivesTheDraftBack(t *testing.T) {
	for _, fails := range []bool{false, true} {
		m := loaded(t, 120, 30)
		open(t, m, 1)
		msgs := m.histories[m.openID].msgs
		replyTo := msgs[len(msgs)-1].ID
		m.pickReply(-1)
		m.input.SetValue("half typed")
		own, _ := m.lastOwn()
		m.edit(own)
		m.input.SetValue("new text")

		enter := tea.KeyPressMsg{Code: tea.KeyEnter}
		_, save := m.Update(enter)
		draftBack := func(when string) {
			t.Helper()
			if m.editing != 0 || m.input.Value() != "half typed" || m.replyTo != replyTo {
				t.Errorf("fails=%v, %s: editing %d, composer %q replying to %d", fails, when, m.editing, m.input.Value(), m.replyTo)
			}
		}
		draftBack("while saving")
		if _, cmd := m.Update(enter); cmd != nil {
			t.Fatal("enter while the edit is saved sent the draft")
		}

		result := save().(editedMsg)
		if fails {
			result.err = errors.New("MESSAGE_EDIT_TIME_EXPIRED")
		}
		m.Update(result)
		if !fails {
			draftBack("after the save")
			continue
		}
		if m.editing != own.ID || m.input.Value() != "new text" {
			t.Fatalf("after the failure: editing %d, composer %q", m.editing, m.input.Value())
		}
		m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		draftBack("after cancelling the failed edit")
	}
}

// A save makes enter wait in its own chat only, until every save there is
// done, however saves in other chats overlap with it.
func TestSaveBlocksOnlyItsChat(t *testing.T) {
	m := loaded(t, 120, 30)
	enter := tea.KeyPressMsg{Code: tea.KeyEnter}
	saveIn := func(i int) tea.Cmd {
		t.Helper()
		open(t, m, i)
		own, ok := m.lastOwn()
		if !ok {
			t.Fatalf("chat %d has no message of its own", i)
		}
		m.edit(own)
		m.input.SetValue("edited")
		_, cmd := m.Update(enter)
		return cmd
	}
	saveA := saveIn(1)
	saveIn(2) // still saving when A's result arrives

	open(t, m, 1)
	m.input.SetValue("hi")
	if _, cmd := m.Update(enter); cmd != nil {
		t.Fatal("enter sent while this chat's edit is saved")
	}
	open(t, m, 3)
	m.input.SetValue("hi")
	if _, cmd := m.Update(enter); cmd == nil {
		t.Error("enter waited in a chat with nothing being saved")
	}
	m.Update(saveA())
	open(t, m, 1)
	m.input.SetValue("hi")
	if _, cmd := m.Update(enter); cmd == nil {
		t.Error("enter still waits after the save is done")
	}
}

// A message that got shorter must not leave the scrolled pane blank.
func TestShrunkContentStaysOnScreen(t *testing.T) {
	m := loaded(t, 120, 30)
	open(t, m, 1)
	m.scroll = 1000 // far above what is loaded, as after a long message shrank
	if screenRow(m, "anyone else seeing the NAS") < 0 {
		t.Error("the conversation is blank")
	}
}
