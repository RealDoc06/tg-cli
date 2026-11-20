package tui

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
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
	if slices.ContainsFunc(m.histories[chat.ID].msgs, func(o core.Message) bool { return o.ID == old.ID }) {
		t.Error("an edit of an unloaded older message was added to the history")
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

// A loaded photo is drawn in place of its "[photo]" label, as placeholder
// cells that keep every row at the width of the terminal.
func TestPhotoIsDrawnInTheConversation(t *testing.T) {
	m := loaded(t, 120, 50)
	m.graphics = true
	open(t, m, 1) // Homelab, with one photo
	m.images = map[imageKey]shown{}
	upload(m, m.loadPictures())

	var photo shown
	for _, msg := range m.histories[m.openID].msgs {
		if msg.Media == core.MediaPhoto {
			photo = m.images[imageKey{msg.ChatID, msg.ID}]
		}
	}
	if photo.id == 0 || photo.cols > photoCols || photo.rows > photoRows {
		t.Fatalf("photo is %+v", photo)
	}
	rows := strings.Split(m.View().Content, "\n")
	drawn := 0
	for i, row := range rows {
		if strings.Contains(row, fmt.Sprintf("\x1b[38;5;%dm\U0010EEEE", photo.id)) {
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

// A sticker replaces its label at sticker size. A video stays labeled, since
// the label tells its length, and is drawn below it.
func TestStickerAndVideoAreDrawn(t *testing.T) {
	cases := []struct {
		chat       int
		label      string
		cols, rows int
		keepsLabel bool
	}{
		{2, "[sticker 😄]", stickerCols, stickerRows, false},  // Bob Chen
		{4, "[sticker ❤️]", stickerCols, stickerRows, false}, // Chiara, animated
		{1, "[video 0:02]", photoCols, photoRows, true},      // Homelab
	}
	for _, c := range cases {
		m := loaded(t, narrowWidth-4, 40) // only the conversation shows
		m.graphics = true
		open(t, m, c.chat)
		m.images = map[imageKey]shown{}
		upload(m, m.loadPictures())

		h := m.histories[m.openID]
		last := h.msgs[len(h.msgs)-1]
		img := m.images[imageKey{last.ChatID, last.ID}]
		if img.id == 0 || img.cols > c.cols || img.rows > c.rows {
			t.Errorf("%s is drawn as %+v", c.label, img)
		}
		if got := strings.Contains(ansi.Strip(m.View().Content), c.label); got != c.keepsLabel {
			t.Errorf("%s: label shown = %v, want %v", c.label, got, c.keepsLabel)
		}
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
		load := m.loadPictures()
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

// In kitty a paste arrives as the list of types on the clipboard, which tg
// then reads through the terminal. That works over SSH, where the system's
// clipboard tools see the remote machine's clipboard.
func TestTerminalPasteAttachesImage(t *testing.T) {
	m := loaded(t, 120, 30)
	open(t, m, 1)
	m.termClip = true
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	packet := func(meta, payload string) tea.Cmd {
		return m.update(uv.UnknownOscEvent("\x1b]5522;type=read:" + meta + ";" + b64(payload) + "\x1b\\"))
	}
	// reply answers the request the terminal received, echoing its id.
	reply := func(request tea.Cmd, mime string, chunks ...string) {
		t.Helper()
		if request == nil {
			t.Fatal("nothing was requested")
		}
		id := regexp.MustCompile(`id=(\d+)`).FindStringSubmatch(fmt.Sprint(request()))[1]
		packet("status=OK:id="+id, "")
		for _, c := range chunks {
			packet("status=DATA:id="+id+":mime="+b64(mime), c)
		}
		packet("status=DONE:id="+id, "")
	}

	// A paste event lists the types, tg asks for the image with its password.
	packet("status=OK:pw="+b64("once"), "")
	packet("status=DATA:mime="+b64("."), "image/png text/plain")
	request := packet("status=DONE", "")
	if raw := fmt.Sprint(request()); !strings.Contains(raw, b64("image/png")) || !strings.Contains(raw, b64("once")) {
		t.Errorf("request %q does not ask for the image with the paste's password", raw)
	}
	reply(request, "image/png", "\x89PNG first chunk,", " second chunk")
	if string(m.attach.Photo) != "\x89PNG first chunk, second chunk" || m.readingClipboard() {
		t.Errorf("attached %q", m.attach.Photo)
	}

	packet("status=OK", "")
	packet("status=DATA:mime="+b64("."), "text/plain")
	reply(packet("status=DONE", ""), "text/plain", "hello")
	if m.input.Value() != "hello" {
		t.Errorf("pasted text %q", m.input.Value())
	}

	// A paste cancelled with esc before its data arrived attaches nothing.
	m.input.Reset()
	packet("status=OK:pw="+b64("again"), "")
	packet("status=DATA:mime="+b64("."), "image/png")
	request = packet("status=DONE", "")
	m.key(tea.KeyPressMsg{Code: tea.KeyEscape})
	reply(request, "image/png", "\x89PNG late")
	if string(m.attach.Photo) == "\x89PNG late" {
		t.Error("a cancelled paste was attached")
	}

	// Without a clipboard of its own, as over SSH, ctrl+v points to kitty's
	// paste instead of asking kitty, which would prompt the user.
	if cmd := m.update(clipboardMsg{seq: m.attachSeq, err: errors.New("no clipboard here")}); cmd != nil {
		t.Error("ctrl+v asked the terminal for its clipboard")
	}
	if !strings.Contains(m.status, "kitty's paste key") {
		t.Errorf("status %q", m.status)
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
	cmd := m.update(tea.KeyPressMsg{Code: tea.KeyEnter})
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

	// While editing, picking a reply would throw the changes away.
	own, _ := m.lastOwn()
	m.startEdit(own)
	m.Update(up)
	if m.editing != own.ID || m.replyTo != 0 {
		t.Fatalf("ctrl+up during an edit: editing %d, reply to %d", m.editing, m.replyTo)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	m.Update(up)
	for _, r := range "on it" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	cmd := m.update(tea.KeyPressMsg{Code: tea.KeyEnter})
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
	m.startEdit(msg)
	if m.input.Value() != msg.Text {
		t.Fatalf("composer holds %q", m.input.Value())
	}
	if cmd := m.update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
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
		m.startEdit(own)
		m.input.SetValue("new text")

		enter := tea.KeyPressMsg{Code: tea.KeyEnter}
		save := m.update(enter)
		draftBack := func(when string) {
			t.Helper()
			if m.editing != 0 || m.input.Value() != "half typed" || m.replyTo != replyTo {
				t.Errorf("fails=%v, %s: editing %d, composer %q replying to %d", fails, when, m.editing, m.input.Value(), m.replyTo)
			}
		}
		draftBack("while saving")
		if cmd := m.update(enter); cmd != nil {
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
		m.startEdit(own)
		m.input.SetValue("edited")
		cmd := m.update(enter)
		return cmd
	}
	saveA := saveIn(1)
	saveIn(2) // still saving when A's result arrives

	open(t, m, 1)
	m.input.SetValue("hi")
	if cmd := m.update(enter); cmd != nil {
		t.Fatal("enter sent while this chat's edit is saved")
	}
	open(t, m, 3)
	m.input.SetValue("hi")
	if cmd := m.update(enter); cmd == nil {
		t.Error("enter waited in a chat with nothing being saved")
	}
	m.Update(saveA())
	open(t, m, 1)
	m.input.SetValue("hi")
	if cmd := m.update(enter); cmd == nil {
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

// A click on a chat's rows opens that chat, a click on the gap between two
// chats opens neither.
func TestClickOpensTheChatUnderIt(t *testing.T) {
	m := loaded(t, 120, 30)
	second := m.visible()[1]
	y := screenRow(m, second.Title)
	m.Update(tea.MouseClickMsg{X: 10, Y: y + 2, Button: tea.MouseLeft}) // the gap below it
	if m.openID != 0 {
		t.Fatalf("a click on the gap opened chat %d", m.openID)
	}
	// Below the last chat drawn, where the next one would go if it fit.
	if rows := m.listRows(); len(m.visible()) > rows {
		m.Update(tea.MouseClickMsg{X: 10, Y: 1 + chatStride*rows, Button: tea.MouseLeft})
		if m.openID != 0 {
			t.Fatalf("a click below the list opened chat %d, which is not drawn", m.openID)
		}
	} else {
		t.Fatal("the demo chats are expected to overflow the list")
	}
	m.Update(tea.MouseClickMsg{X: 10, Y: y + 1, Button: tea.MouseLeft})
	if m.openID != second.ID {
		t.Errorf("opened chat %d, want %q", m.openID, second.Title)
	}

	// Too short to draw any chat: nothing is there to click.
	tiny := loaded(t, 120, 3)
	tiny.Update(tea.MouseClickMsg{X: 10, Y: 1, Button: tea.MouseLeft})
	if tiny.openID != 0 {
		t.Errorf("a click in a list too short for any chat opened chat %d", tiny.openID)
	}
}

// Messages are packed unless spacing is on, which sets them apart.
func TestSpacingSetsMessagesApart(t *testing.T) {
	m := loaded(t, 120, 30)
	open(t, m, 1)
	blanks := func() int {
		lines := m.messageLines(m.histories[m.openID], 80)
		return len(slices.DeleteFunc(lines, func(l msgLine) bool { return l.text != "" }))
	}
	if n := blanks(); n != 0 {
		t.Errorf("packed by default, got %d blank lines", n)
	}
	m.spaced = true
	if blanks() == 0 {
		t.Error("spacing on left no gap between messages")
	}
}

// An edit changes the text only: it cannot start while a pasted image is
// still being read, which would attach to it.
func TestEditWaitsForTheClipboard(t *testing.T) {
	m := loaded(t, 120, 30)
	open(t, m, 1)
	m.startRead()
	own, _ := m.lastOwn()
	m.startEdit(own)
	if m.editing != 0 || m.status != waitClipboard {
		t.Errorf("edit started during a clipboard read: editing %d, status %q", m.editing, m.status)
	}
}

// Animations take megabytes in the terminal, which drops pictures on its own
// past its quota. The TUI frees the oldest first, so it knows they are gone
// and loads them again when they come back into view.
func TestOldestPicturesAreFreedPastTheBudget(t *testing.T) {
	m := loaded(t, 120, 30)
	big := imageBudget / 3
	for i := 1; i <= 4; i++ {
		key := imageKey{1, i}
		m.update(uploadedMsg{key, shown{id: i, cols: 4, rows: 2, bytes: big, seq: m.loading(key)}})
	}
	if _, kept := m.images[imageKey{1, 1}]; kept {
		t.Error("the oldest picture is still counted on")
	}
	for i := 2; i <= 4; i++ {
		if _, kept := m.images[imageKey{1, i}]; !kept {
			t.Errorf("picture %d was freed too", i)
		}
	}
	if m.imageBytes != 3*big {
		t.Errorf("%d bytes counted, want %d", m.imageBytes, 3*big)
	}
	if !slices.Contains(m.free, 1) {
		t.Error("the oldest picture stays in the terminal")
	}
}

// An edit can replace the media of a message, so its picture is freed and
// loaded again. A load from before the edit must not land on top.
func TestEditedPictureIsFreed(t *testing.T) {
	m := loaded(t, 120, 50)
	m.graphics = true
	open(t, m, 1) // Homelab, with a photo
	h := m.histories[m.openID]
	photo := h.msgs[slices.IndexFunc(h.msgs, func(msg core.Message) bool { return msg.Media == core.MediaPhoto })]
	key := imageKey{photo.ChatID, photo.ID}
	m.images = map[imageKey]shown{}
	m.imageBytes = 0
	upload(m, m.loadPictures())
	old := m.images[key]
	if old.id == 0 || m.imageBytes < old.bytes {
		t.Fatalf("photo is %+v, %d bytes counted", old, m.imageBytes)
	}
	before := m.imageBytes

	m.update(eventMsg(core.Event{Kind: core.EventEdit, Message: photo}))
	if _, seen := m.images[key]; seen || m.imageBytes != before-old.bytes || !slices.Contains(m.free, old.id) {
		t.Errorf("after the edit: %+v, %d bytes counted, freeing %v", m.images[key], m.imageBytes, m.free)
	}

	// A load started before the edit finishes after it.
	seq := m.loading(key)
	m.loading(key) // the load the edit started
	m.update(imageMsg{key: key, seq: seq, frames: [][]byte{{1}}, cols: 4, rows: 2})
	if m.images[key].id != 0 {
		t.Error("an outdated picture landed")
	}
}

// Leaving a chat stops loading its pictures, and they load again on return.
func TestLeavingAChatStopsItsPictures(t *testing.T) {
	m := loaded(t, 120, 30)
	m.graphics = true
	open(t, m, 1)
	left := m.view
	open(t, m, 0)
	if left.Err() == nil {
		t.Fatal("the pictures of the chat left still load")
	}
	key := imageKey{1, 1}
	m.images[key] = shown{}
	m.update(imageMsg{key: key, retry: true})
	if _, seen := m.images[key]; seen {
		t.Error("a stopped picture would never load again")
	}
}

// A new picture never takes the ID of one still shown, nor one freed a
// moment ago: its delete may reach the terminal after the new upload.
func TestNewPicturesTakeFreeIDs(t *testing.T) {
	m := loaded(t, 120, 30)
	m.images = map[imageKey]shown{}
	for id := 1; id <= 3; id++ {
		m.images[imageKey{1, id}] = shown{id: id}
	}
	m.freedAt[4] = time.Now()
	if id := m.nextImage(); id != 5 {
		t.Errorf("took ID %d, want 5", id)
	}
}
