package tui

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
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

// upload runs the commands that load and upload pictures, and only those.
func upload(m *model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	switch msg := msg.(type) {
	case imageMsg, uploadedMsg:
		upload(m, m.update(msg))
		return
	}
	// Batches and sequences are lists of commands.
	if v := reflect.ValueOf(msg); v.Kind() == reflect.Slice {
		for i := range v.Len() {
			if c, ok := v.Index(i).Interface().(tea.Cmd); ok {
				upload(m, c)
			}
		}
	}
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

// A loaded photo is drawn in place of its "[photo]" label, as placeholder
// cells that keep every row at the width of the terminal.
func TestPhotoIsDrawnInTheConversation(t *testing.T) {
	m := loaded(t, 120, 30)
	m.graphics = true
	open(t, m, 1) // Homelab, with one photo
	m.images = map[imageKey]shown{}
	upload(m, m.loadPhotos())

	var photo shown
	for key, img := range m.images {
		if key.msg != 0 {
			photo = img
		}
	}
	if photo.id == 0 || photo.cols > photoCols || photo.rows > photoRows {
		t.Fatalf("photo is %+v", photo)
	}
	rows := strings.Split(m.View().Content, "\n")
	drawn := 0
	for i, row := range rows {
		if strings.ContainsRune(row, '\U0010EEEE') {
			drawn++
		}
		if w := ansi.StringWidth(row); w != 120 {
			t.Errorf("row %d is %d cells", i, w)
		}
	}
	if drawn != photo.rows {
		t.Errorf("%d rows hold the photo, want %d", drawn, photo.rows)
	}
	if strings.Contains(ansi.Strip(m.View().Content), "Marco: [photo]") {
		t.Error("the label is still shown next to the photo")
	}
}

// Dropping an image on the terminal pastes its path, escaped like a shell
// word. It must attach the image, and enter must send it with the caption.
func TestPastedImagePathIsSentAsPhoto(t *testing.T) {
	path := filepath.Join(t.TempDir(), "my photo.png")
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewNRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	m := loaded(t, 120, 30)
	open(t, m, 1)
	pasted := path
	if filepath.Separator == '/' {
		pasted = strings.ReplaceAll(path, " ", `\ `) // how POSIX shells escape it
	}
	m.Update(tea.PasteMsg{Content: pasted})
	if m.attach.File != path || m.input.Value() != "" {
		t.Fatalf("attached %q, input %q", m.attach.File, m.input.Value())
	}
	for _, r := range "look" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m.update(m.key(tea.KeyPressMsg{Code: tea.KeyEnter})())

	h := m.histories[m.openID].msgs
	if last := h[len(h)-1]; last.Text != "look" || last.Media != core.MediaPhoto {
		t.Errorf("sent %q with media %q", last.Text, last.Media)
	}
	if m.attached() {
		t.Error("the attachment is still there after sending")
	}
}

// An attachment's name is shown in the prompt. A long one must not push the
// composer past the pane, which made the text input panic.
func TestLongAttachmentNameFits(t *testing.T) {
	m := loaded(t, 80, 24)
	open(t, m, 1)
	m.attach = core.SendRequest{File: "/tmp/" + strings.Repeat("screenshot", 8) + ".png"}
	for i, row := range strings.Split(m.View().Content, "\n") {
		if w := ansi.StringWidth(row); w != 80 {
			t.Errorf("row %d is %d cells", i, w)
		}
	}
}

// Reading the clipboard takes a moment. A result that arrives after the user
// switched chats or attached something else must be dropped.
func TestStaleClipboardReadIsDropped(t *testing.T) {
	m := loaded(t, 120, 30)
	open(t, m, 1)
	seq := m.attachSeq // ctrl+v pressed, then the user moves on
	open(t, m, 2)
	m.Update(clipboardMsg{seq: seq, png: []byte("old")})
	m.Update(clipboardMsg{seq: seq, text: "old"})
	if m.attached() || m.input.Value() != "" {
		t.Errorf("a read from another chat landed: %+v, %q", m.attach, m.input.Value())
	}

	m.pasteImage()
	seq = m.attachSeq
	m.setAttach(core.SendRequest{File: "/dropped.png"})
	m.Update(clipboardMsg{seq: seq, png: []byte("old")})
	if m.attach.File != "/dropped.png" {
		t.Error("an older clipboard read replaced the dropped file")
	}

	m.pasteImage()
	seq = m.attachSeq
	m.pasteImage() // ctrl+v pressed again before the first read returned
	m.Update(clipboardMsg{seq: seq, png: []byte("old")})
	if m.attach.File != "/dropped.png" {
		t.Error("the first of two clipboard reads was attached")
	}
}

// Only an empty terminal paste stands for an image-only clipboard. Pasted
// blank text must not attach whatever image the system clipboard holds.
func TestBlankPasteDoesNotReadTheClipboard(t *testing.T) {
	m := loaded(t, 120, 30)
	open(t, m, 1)
	seq := m.attachSeq
	m.Update(tea.PasteMsg{Content: " \n"})
	if m.attachSeq != seq {
		t.Error("a blank paste read the clipboard")
	}
}

// Clipboard text that is only blank must be pasted, not taken for the empty
// paste of an image-only clipboard, which read the clipboard again forever.
func TestBlankClipboardTextDoesNotReadAgain(t *testing.T) {
	m := loaded(t, 120, 30)
	open(t, m, 1)
	if cmd := m.update(clipboardMsg{seq: m.attachSeq, text: "\n"}); cmd != nil {
		t.Error("blank clipboard text started another command")
	}
}

// Reading older messages, a photo that loads must not move the text in view,
// whether it is in view or scrolled above it by the time it arrives.
func TestPhotoLoadKeepsTheView(t *testing.T) {
	for _, above := range []bool{false, true} {
		m := loaded(t, 120, 8)
		m.graphics = true
		open(t, m, 1)
		m.images = map[imageKey]shown{}
		h, rows := m.histories[m.openID], m.messageRows()
		_, rw := m.layout()
		lines := m.messageLines(h, rw)
		photo := slices.IndexFunc(lines, func(l msgLine) bool { return l.msg >= 0 && h.msgs[l.msg].Media == core.MediaPhoto })
		m.scroll = len(lines) - rows - (photo - 1) // the photo is the second line in view
		load := m.loadPhotos()
		if above {
			m.scroll = len(lines) - rows - (photo + 1) // the photo is just above the view
		}
		if m.scroll <= 0 {
			t.Fatalf("scroll %d, the demo chat is too short for this test", m.scroll)
		}
		top := func() string { return ansi.Strip(strings.Split(m.View().Content, "\n")[1]) }
		before := top()
		upload(m, load)
		if len(m.images) == 0 {
			t.Fatal("no photo was loaded")
		}
		if after := top(); after != before {
			t.Errorf("above=%v: top line went from %q to %q", above, before, after)
		}
	}
}

// A send that fails gives the draft back, unless the composer changed since:
// then the draft would land on top of newer work, or in another chat.
func TestFailedSendRestoresTheDraft(t *testing.T) {
	m := loaded(t, 120, 30)
	open(t, m, 1)
	failed := func() sentMsg {
		m.setAttach(core.SendRequest{}) // the composer is cleared for the send
		return sentMsg{seq: m.attachSeq, req: core.SendRequest{Text: "look", File: "/a.png"}, err: errors.New("offline")}
	}

	m.Update(failed())
	if m.input.Value() != "look" || m.attach.File != "/a.png" {
		t.Errorf("draft not restored: %q, %+v", m.input.Value(), m.attach)
	}

	m.input.Reset()
	msg := failed()
	m.pasteImage() // ctrl+v while the send was in flight
	m.Update(msg)
	if m.input.Value() != "" || m.attached() {
		t.Error("the draft was restored over a newer paste")
	}

	msg = failed()
	open(t, m, 2)
	m.Update(msg)
	if m.input.Value() != "" || m.attached() {
		t.Error("the draft was restored into another chat")
	}
}

// Enter during a clipboard read would send what the read is about to
// replace. It waits instead, and esc cancels a read that never returns.
func TestEnterWaitsForTheClipboard(t *testing.T) {
	m := loaded(t, 120, 30)
	open(t, m, 1)
	m.setAttach(core.SendRequest{File: "/a.png"})
	m.pasteImage() // ctrl+v with screenshot B in the clipboard
	if cmd := m.key(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil || m.attach.File != "/a.png" {
		t.Fatal("enter sent while the clipboard was being read")
	}

	m.key(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.focus != focusInput || m.readingClipboard() {
		t.Fatal("esc did not cancel the read")
	}
	if m.key(tea.KeyPressMsg{Code: tea.KeyEnter}) == nil {
		t.Error("enter does not send once the read is cancelled")
	}

	m.pasteImage()
	m.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.Update(clipboardMsg{seq: m.attachSeq, png: []byte("b")})
	if m.readingClipboard() || string(m.attach.Photo) != "b" || m.status != "" {
		t.Errorf("the read did not finish cleanly, status %q", m.status)
	}
}
