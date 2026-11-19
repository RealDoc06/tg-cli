package tui

import (
	"context"
	"errors"
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
	m := &model{
		ctx: context.Background(), be: be, version: "test",
		histories: map[int64]*history{}, avatars: map[int64]int{},
		input: newInput("> "), filter: newInput("/ "),
	}
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
		for _, opened := range []bool{false, true} {
			if opened {
				open(t, m, 1)
			}
			rows := strings.Split(m.View().Content, "\n")
			if len(rows) != size[1] {
				t.Errorf("%v opened=%v: %d rows", size, opened, len(rows))
			}
			for i, row := range rows {
				if w := ansi.StringWidth(row); w != size[0] {
					t.Errorf("%v opened=%v: row %d is %d cells: %q", size, opened, i, w, ansi.Strip(row))
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
