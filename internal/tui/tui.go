// Package tui is the interactive client: chat list on the left, the open
// conversation and a composer on the right. It only talks to a core.Backend,
// so it runs unchanged against Telegram and against the demo data.
package tui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	textclip "github.com/atotto/clipboard"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/RealDoc06/tg-cli/internal/clipboard"
	"github.com/RealDoc06/tg-cli/internal/core"
	"github.com/RealDoc06/tg-cli/internal/termimg"
)

const (
	chatPage    = 100 // chats fetched per request
	maxChats    = 500 // chats preloaded so the filter can find them
	historyPage = 50
	avatarCols  = 4
	avatarRows  = 2
	avatarPx    = 96
	photoCols   = 36 // largest photo in a conversation, in cells
	photoRows   = 14
	stickerCols = 16 // largest sticker, in cells
	stickerRows = 8
	narrowWidth = 70 // below this only one pane is shown at a time
)

type focus int

const (
	focusChats focus = iota
	focusFilter
	focusInput
)

// history is the loaded part of one conversation.
type history struct {
	msgs    []core.Message
	loaded  bool // first page arrived
	done    bool // nothing older on the server
	loading bool
}

type model struct {
	ctx     context.Context
	be      core.Backend
	version string
	account string // shown in the status line when not empty
	// graphics is set when the terminal can draw pictures.
	graphics bool
	st       styles
	spaced   bool // gaps between messages, see Look

	width, height int
	focus         focus

	chats     []core.Chat
	chatsDone bool
	sel       int // index into visible()
	top       int // first visible chat row

	openID    int64 // 0 when no chat is open
	histories map[int64]*history
	scroll    int // lines scrolled up from the newest message

	input  textinput.Model
	filter textinput.Model
	// attach is the picture or file pasted into the composer, sent with the
	// next message. Its Text is unused. Change it with setAttach.
	attach core.SendRequest
	// attachSeq counts changes to attach. A clipboard read started before
	// the latest one is stale: the user moved on.
	attachSeq int
	// pasting is the attachSeq of the clipboard read in progress. While it
	// is current, enter waits: it would send what the read is replacing.
	pasting int
	// termClip is set when the terminal reports pastes with their clipboard
	// (OSC 5522), reply is the answer it is sending.
	termClip bool
	reply    *termReply
	// The composer replies to replyTo, or saves an edit of editing instead
	// of sending. At most one is set, 0 means none.
	replyTo, editing int
	editFrom         string        // the text the edit started from
	saving           map[int64]int // edits being saved, per chat
	stash            *stash        // the composer as it was before an edit, nil for none
	menu             *menu         // the open context menu, nil when there is none

	// images holds the pictures uploaded to the terminal. An entry with ID
	// 0 is loading, or failed and shows as text.
	images    map[imageKey]shown
	lastImage int
	// stored lists the pictures in the order they reached the terminal, and
	// imageBytes is what they take there. Past imageBudget the oldest are
	// freed, before the terminal drops pictures on its own.
	stored     []stored
	imageBytes int
	// free lists terminal images to delete, sent after the update. freedAt
	// tells when, so a delete still on its way never hits a new picture.
	free    []int
	freedAt map[int]time.Time
	loadSeq int
	// uploading holds the IDs of pictures on their way to the terminal.
	uploading map[int]bool
	// view is cancelled when another chat opens, which stops loading the
	// pictures of the one left. jobs bounds how many load at once.
	view  context.Context
	leave context.CancelFunc
	jobs  chan struct{}

	status string // an error, shown instead of the hints
	note   string // a confirmation, like "copied", shown the same way
}

// stash is what the composer held before an edit: its text and the reply it
// was for. It comes back when the edit is cancelled or saved.
type stash struct {
	text    string
	replyTo int
}

// menu is the context menu of one message, opened with a right click.
type menu struct {
	msg   core.Message
	items []menuItem
	x, y  int // top left cell on screen
	sel   int
}

type menuItem struct {
	label string
	run   func(m *model, msg core.Message) tea.Cmd
}

// imageKey names a picture: the media of a message, or the avatar of a chat
// when msg is 0.
type imageKey struct {
	chat int64
	msg  int
}

// shown is a picture uploaded to the terminal as image id, drawn over a
// cols x rows cell area, which takes about bytes there. seq tells loads of
// the same picture apart: only the latest may land.
type shown struct{ id, cols, rows, bytes, seq int }

// stored is a picture that reached the terminal.
type stored struct {
	key imageKey
	id  int
}

// imageBudget is how much the TUI lets the terminal hold of its pictures.
// kitty and Ghostty keep 320 MB, and drop the oldest past that.
const imageBudget = 160 << 20

type (
	chatsMsg struct {
		chats []core.Chat
		err   error
	}
	historyMsg struct {
		chatID int64
		msgs   []core.Message
		older  bool
		err    error
	}
	// sentMsg reports a send. seq is attachSeq once the composer was
	// cleared for it.
	sentMsg struct {
		seq int
		req core.SendRequest
		msg core.Message
		err error
	}
	// imageMsg carries a picture ready to upload as PNG frames, more than
	// one for an animation, each shown for gap. frames is nil when it could
	// not be loaded.
	imageMsg struct {
		key        imageKey
		seq        int
		frames     [][]byte
		gap        time.Duration
		cols, rows int
		// retry is set when loading stopped because the chat was left: the
		// picture loads again when it comes back into view.
		retry bool
	}
	// uploadedMsg follows the upload of a picture to the terminal.
	uploadedMsg struct {
		key imageKey
		img shown
	}
	// clipboardMsg carries what the system clipboard held: an image, or
	// text when it had none. seq is attachSeq when the read started.
	clipboardMsg struct {
		seq  int
		png  []byte
		text string
		err  error
	}
	editedMsg struct {
		chatID int64
		id     int
		text   string
		err    error
	}
	errMsg   struct{ err error }
	eventMsg core.Event
)

// Look is how the TUI is drawn, as config.json sets it.
type Look struct {
	Theme  string // one of Themes, "" for the default
	Spaced bool   // a gap between messages of different senders or moments
}

// Run starts the TUI and blocks until the user quits. account, when not
// empty, is shown so it is clear whose chats these are.
func Run(ctx context.Context, be core.Backend, version, account string, look Look) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	m := newModel(ctx, be, look.Theme)
	m.spaced = look.Spaced
	m.version, m.account, m.graphics = version, account, termimg.Supported()
	p := tea.NewProgram(m, tea.WithContext(ctx))
	go func() {
		if err := be.Watch(ctx, func(ev core.Event) { p.Send(eventMsg(ev)) }); err != nil && ctx.Err() == nil {
			p.Send(errMsg{err})
		}
	}()
	_, err := p.Run()
	fmt.Fprint(os.Stdout, clipboard.PasteEventsOff)

	if ids := slices.Collect(func(yield func(int) bool) {
		for _, img := range m.images {
			if img.id != 0 && !yield(img.id) {
				return
			}
		}
	}); len(ids) > 0 {
		fmt.Fprint(os.Stdout, termimg.DeleteAll(ids))
	}
	if err == tea.ErrProgramKilled && ctx.Err() != nil {
		return nil
	}
	return err
}

// newModel sets up an empty model drawn with the named theme. A name that
// is not a theme falls back to the default and says so.
func newModel(ctx context.Context, be core.Backend, theme string) *model {
	t, ok := FindTheme(theme)
	m := &model{
		ctx: ctx, be: be, histories: map[int64]*history{}, images: map[imageKey]shown{}, saving: map[int64]int{},
		jobs: make(chan struct{}, 3), freedAt: map[int]time.Time{}, uploading: map[int]bool{},
	}
	m.view, m.leave = context.WithCancel(ctx)
	if !ok {
		m.status = fmt.Sprintf("no theme %q, pick one of %s with `tg config theme <name>`", theme, strings.Join(ThemeNames(), ", "))
	}
	m.st = newStyles(t)
	m.input, m.filter = m.newInput("> "), m.newInput("/ ")
	return m
}

func (m *model) newInput(prompt string) textinput.Model {
	in := textinput.New()
	in.Prompt = prompt
	in.SetVirtualCursor(false)
	// Every color comes from the theme: the defaults assume a dark terminal.
	styles := in.Styles()
	styles.Focused = textinput.StyleState{Text: m.st.text, Placeholder: m.st.muted, Suggestion: m.st.muted, Prompt: m.st.accent}
	styles.Blurred = textinput.StyleState{Text: m.st.muted, Placeholder: m.st.muted, Suggestion: m.st.muted, Prompt: m.st.muted}
	styles.Cursor.Color = m.st.theme.Accent
	in.SetStyles(styles)
	return in
}

// Init enters the alternate screen before anything else, then loads the
// chats. Bubble Tea would only switch on its first frame, and kitty keeps one
// image store per screen: a picture sent before the switch, like an initials
// badge that is ready at once, would never show.
func (m *model) Init() tea.Cmd {
	return tea.Sequence(
		tea.Raw(ansi.SetModeAltScreenSaveCursor),
		tea.Batch(m.loadChats(nil), tea.Raw(clipboard.PasteEventsOn)),
	)
}

// Update handles a message, then fetches the pictures that came into view,
// whatever moved them there.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Avatars too: on a narrow terminal the list shows again by a focus
	// change alone.
	cmds := []tea.Cmd{m.update(msg), m.loadPictures(), m.loadAvatars()}
	if m.imageBytes > imageBudget {
		m.evict() // pictures that left the view can go now
	}
	if len(m.free) > 0 {
		now := time.Now()
		for _, id := range m.free {
			m.freedAt[id] = now
		}
		cmds = append(cmds, tea.Raw(termimg.DeleteAll(m.free)))
		m.free = nil
	}
	return m, tea.Batch(cmds...)
}

func (m *model) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.menu = nil // it was placed for the old size
		return tea.Batch(m.afterMove(), m.afterScroll())

	case tea.KeyPressMsg:
		return m.key(msg)

	case tea.MouseWheelMsg:
		m.menu = nil
		delta := 1
		if msg.Button == tea.MouseWheelUp {
			delta = -1
		}
		if lw, _ := m.layout(); msg.X < lw {
			m.sel += delta
			return m.afterMove()
		}
		m.scroll -= delta * 3
		return m.afterScroll()

	case tea.MouseClickMsg:
		lw, _ := m.layout()
		if m.menu != nil {
			if i := msg.Y - m.menu.y; msg.Button == tea.MouseLeft && i >= 0 && i < len(m.menu.items) &&
				msg.X >= m.menu.x && msg.X < m.menu.x+menuWidth(m.menu.items) {
				m.menu.sel = i
				return m.runMenu()
			}
			m.menu = nil // a click elsewhere closes it, a right click opens another
			if msg.Button != tea.MouseRight {
				return nil
			}
		}
		if msg.Y < 1 || msg.Y >= m.height-1 {
			return nil
		}
		if msg.Button == tea.MouseRight {
			if msg.X >= lw {
				m.openMenu(msg.X, msg.Y)
			}
			return nil
		}
		if msg.Button != tea.MouseLeft {
			return nil
		}
		if msg.X >= lw {
			return m.setFocus(focusInput)
		}
		if k := (msg.Y - 1) / chatStride; (msg.Y-1)%chatStride < 2 && k < m.chatsDrawn() && m.top+k < len(m.visible()) {
			m.sel = m.top + k
			return m.openSelected()
		}
		return nil

	case chatsMsg:
		if msg.err != nil {
			m.status = msg.err.Error()
			return nil
		}
		before := len(m.chats)
		for _, c := range msg.chats {
			if m.chat(c.ID) == nil { // pages can overlap when chats move
				m.chats = append(m.chats, c)
			}
		}
		m.chatsDone = len(msg.chats) < chatPage || len(m.chats) >= maxChats || len(m.chats) == before
		cmds := []tea.Cmd{m.afterMove()}
		if !m.chatsDone {
			cmds = append(cmds, m.loadChats(&m.chats[len(m.chats)-1]))
		}
		return tea.Batch(cmds...)

	case historyMsg:
		h := m.histories[msg.chatID]
		h.loading = false
		if msg.err != nil {
			m.status = msg.err.Error()
			return nil
		}
		h.loaded = true
		if msg.older {
			h.done = len(msg.msgs) < historyPage
			h.msgs = append(msg.msgs, h.msgs...)
			return nil
		}
		h.done = len(msg.msgs) < historyPage
		h.msgs = msg.msgs
		return m.markRead(msg.chatID)

	case sentMsg:
		if msg.err != nil {
			m.status = "send failed: " + msg.err.Error()
			// Give the message back, so it is not lost, unless the chat
			// changed or something else was written or pasted since.
			if msg.seq == m.attachSeq && m.input.Value() == "" && m.replyTo == 0 && m.editing == 0 {
				m.input.SetValue(msg.req.Text)
				m.replyTo = msg.req.ReplyTo
				if msg.req.File != "" || msg.req.Photo != nil {
					msg.req.Text = ""
					m.setAttach(msg.req)
				}
			}
			return nil
		}
		m.addMessage(msg.msg, nil, false)
		m.scroll = 0
		return nil

	case editedMsg:
		if m.saving[msg.chatID]--; m.saving[msg.chatID] <= 0 {
			delete(m.saving, msg.chatID)
		}
		if m.status == savingNote {
			m.status = ""
		}
		if msg.err != nil {
			m.status = "edit failed: " + msg.err.Error()
			// Put the edit back so it is not lost, over what the composer
			// holds now, unless another chat is open. No edit can be under
			// way in this chat: they wait for the save.
			if original, ok := m.message(msg.id); ok && msg.chatID == m.openID {
				if m.input.Value() != "" {
					m.status += ", esc gives back what you were typing"
				}
				m.stash = &stash{m.input.Value(), m.replyTo}
				m.replyTo, m.editing, m.editFrom = 0, msg.id, original.Text
				m.input.SetValue(msg.text)
				m.input.CursorEnd()
				m.revealMessage(msg.id)
				return m.afterScroll()
			}
			return nil
		}
		// Show the saved text at once. The live updates stay authoritative:
		// they carry this edit and any later one in the order Telegram
		// applied them, and each replaces the whole message.
		if h := m.histories[msg.chatID]; h != nil {
			if i := slices.IndexFunc(h.msgs, func(o core.Message) bool { return o.ID == msg.id }); i >= 0 {
				h.msgs[i].Text, h.msgs[i].Edited = msg.text, true
			}
		}
		if c := m.chat(msg.chatID); c != nil && c.Last != nil && c.Last.ID == msg.id {
			last := *c.Last
			last.Text, last.Edited = msg.text, true
			c.Last = &last
		}
		if msg.chatID == m.openID {
			return m.afterScroll() // a shorter text has fewer lines
		}
		return nil

	case eventMsg:
		ev := core.Event(msg)
		if ev.Kind == core.EventReady {
			return nil
		}
		m.addMessage(ev.Message, &ev.Chat, ev.Kind == core.EventEdit)
		cmds := []tea.Cmd{m.afterMove()}
		if ev.Kind == core.EventEdit && ev.Message.ChatID == m.openID {
			cmds = append(cmds, m.afterScroll()) // the edit can change its line count
		}
		if ev.Message.ChatID == m.openID && !ev.Message.Out {
			cmds = append(cmds, m.markRead(m.openID))
		}
		return tea.Batch(cmds...)

	case imageMsg:
		if cur, ok := m.images[msg.key]; !ok || cur.seq != msg.seq || cur.id != 0 {
			return nil // replaced or dropped while it loaded
		}
		if msg.retry {
			delete(m.images, msg.key)
			return nil
		}
		if msg.frames == nil {
			return nil // stays 0, initials or the "[photo]" label are shown
		}
		// The picture is shown only once it reached the terminal: placeholder
		// cells drawn before their image may stay blank. An animation plays
		// in the terminal from then on, without redraws.
		img := shown{m.nextImage(), msg.cols, msg.rows, termimg.Footprint(msg.frames), msg.seq}
		m.uploading[img.id] = true
		return tea.Sequence(
			tea.Raw(termimg.Transmit(img.id, img.cols, img.rows, msg.frames[0])+termimg.Play(img.id, msg.gap, msg.frames[1:])),
			func() tea.Msg { return uploadedMsg{msg.key, img} },
		)

	case uploadedMsg:
		delete(m.uploading, msg.img.id)
		if cur, ok := m.images[msg.key]; !ok || cur.seq != msg.img.seq || cur.id != 0 {
			m.free = append(m.free, msg.img.id) // replaced or dropped while it uploaded
			return nil
		}
		m.keepView(msg.key.msg, func() { m.images[msg.key] = msg.img })
		m.imageBytes += msg.img.bytes
		m.stored = append(m.stored, stored{msg.key, msg.img.id})
		m.evict()
		return nil

	case clipboardMsg:
		if msg.seq != m.attachSeq {
			return nil // the chat or the attachment changed since
		}
		m.pasting = 0
		if m.status == waitClipboard {
			m.status = ""
		}
		switch {
		case msg.png != nil:
			m.setAttach(core.SendRequest{Photo: msg.png})
		case msg.text != "":
			return m.pasteText(msg.text) // no image, ctrl+v pastes text as before
		case m.termClip && !errors.Is(msg.err, clipboard.ErrNoImage):
			// No clipboard here, as over SSH. kitty's own paste brings the
			// user's clipboard without asking, ctrl+v could only with a prompt.
			m.status = "no clipboard here, paste the image with kitty's paste key (ctrl+shift+v)"
		default:
			m.status = msg.err.Error() // nothing pasted, any attachment stays as it was
		}
		return nil

	case tea.ModeReportMsg:
		if msg.Mode == clipboard.PasteEventsMode {
			m.termClip = !msg.Value.IsNotRecognized() && !msg.Value.IsPermanentlyReset()
		}
		return nil

	case uv.UnknownOscEvent:
		if p, ok := clipboard.ParsePacket(string(msg)); ok {
			return m.termPacket(p)
		}
		return nil

	case tea.PasteMsg:
		// An empty paste is what some terminals send when the clipboard
		// holds only an image.
		if m.focus == focusInput && msg.Content == "" {
			return m.pasteImage()
		}
		if m.focus == focusInput {
			return m.pasteText(msg.Content)
		}

	case errMsg:
		m.status = msg.err.Error()
		return nil
	}

	// Everything else (paste, cursor blink) belongs to the focused input.
	var cmd tea.Cmd
	switch m.focus {
	case focusInput:
		m.input, cmd = m.input.Update(msg)
	case focusFilter:
		m.filter, cmd = m.filter.Update(msg)
	}
	return cmd
}

func (m *model) key(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	m.status, m.note = "", ""
	if key == "ctrl+c" {
		return tea.Quit
	}

	if m.menu != nil {
		n := len(m.menu.items)
		switch key {
		case "up", "k", "shift+tab":
			m.menu.sel = (m.menu.sel + n - 1) % n
		case "down", "j", "tab":
			m.menu.sel = (m.menu.sel + 1) % n
		case "enter", "space":
			return m.runMenu()
		default:
			m.menu = nil
		}
		return nil
	}

	// Navigation shared by the chat list and its filter.
	if m.focus != focusInput {
		switch key {
		case "up", "ctrl+p":
			m.sel--
			return m.afterMove()
		case "down", "ctrl+n":
			m.sel++
			return m.afterMove()
		case "pgup":
			m.sel -= m.listRows()
			return m.afterMove()
		case "pgdown":
			m.sel += m.listRows()
			return m.afterMove()
		}
	}

	switch m.focus {
	case focusChats:
		switch key {
		case "q":
			return tea.Quit
		case "k":
			m.sel--
		case "j":
			m.sel++
		case "g", "home":
			m.sel = 0
		case "G", "end":
			m.sel = len(m.visible()) - 1
		case "/":
			return m.setFocus(focusFilter)
		case "esc":
			m.filter.Reset()
		case "enter", "right", "l":
			return m.openSelected()
		case "tab":
			if m.openID != 0 {
				return m.setFocus(focusInput)
			}
		}
		return m.afterMove()

	case focusFilter:
		switch key {
		case "esc":
			m.filter.Reset()
			m.sel = 0
			return tea.Batch(m.setFocus(focusChats), m.afterMove())
		case "enter":
			return m.openSelected()
		case "tab":
			return m.setFocus(focusChats)
		}
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg)
		m.sel = 0
		return tea.Batch(cmd, m.afterMove())

	default: // focusInput
		switch key {
		case "esc", "tab":
			if m.readingClipboard() {
				m.attachSeq++ // cancels the read, the clipboard helper may hang
				return nil
			}
			if key == "esc" && (m.replyTo != 0 || m.editing != 0) {
				m.cancelDraft()
				return m.afterScroll()
			}
			return m.setFocus(focusChats)
		case "ctrl+up", "alt+up":
			return m.pickReply(-1)
		case "ctrl+down", "alt+down":
			return m.pickReply(1)
		case "up":
			if m.input.Value() == "" && !m.attached() {
				if msg, ok := m.lastOwn(); ok {
					return m.startEdit(msg)
				}
			}
		case "pgup":
			m.scroll += max(1, m.messageRows()/2)
			return m.afterScroll()
		case "pgdown":
			m.scroll -= max(1, m.messageRows()/2)
			return m.afterScroll()
		case "ctrl+v":
			if m.editing != 0 {
				m.status = "an edit changes the text only, esc cancels it"
				return nil
			}
			return m.pasteImage()
		case "backspace":
			if m.input.Value() == "" && m.attached() {
				m.setAttach(core.SendRequest{})
				return nil
			}
		case "enter":
			return m.submit()
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return cmd
	}
}

func (m *model) attached() bool { return m.attach.File != "" || m.attach.Photo != nil }

// savingNote asks to wait while an edit is saved in the open chat.
const savingNote = "saving the edit, a moment"

// submit sends what is in the composer, as a reply when one is picked, or
// saves it as the new text of the message being edited.
func (m *model) submit() tea.Cmd {
	if m.saving[m.openID] > 0 {
		// Wait, so a second enter does not send the draft put back.
		m.status = savingNote
		return nil
	}
	if m.readingClipboard() {
		m.status = waitClipboard
		return nil
	}
	text := strings.TrimSpace(m.input.Value())
	chatID, editing := m.openID, m.editing
	if editing != 0 {
		if text == "" {
			m.status = "a message cannot be empty, esc cancels the edit"
			return nil
		}
		if text == strings.TrimSpace(m.editFrom) {
			m.cancelDraft()
			return nil
		}
		m.saving[chatID]++
		m.cancelDraft()
		return func() tea.Msg {
			return editedMsg{chatID, editing, text, m.be.Edit(m.ctx, chatID, editing, text)}
		}
	}
	req := m.attach
	req.Text, req.ReplyTo = text, m.replyTo
	if req.Text == "" && !m.attached() {
		return nil
	}
	m.input.Reset()
	m.replyTo = 0
	m.setAttach(core.SendRequest{})
	seq := m.attachSeq
	return func() tea.Msg {
		sent, err := m.be.Send(m.ctx, chatID, req)
		return sentMsg{seq, req, sent, err}
	}
}

// startReply makes the composer answer msg, startEdit starts editing it.
func (m *model) startReply(msg core.Message) tea.Cmd {
	if m.editing != 0 {
		m.cancelDraft()
	}
	m.replyTo = msg.ID
	m.revealMessage(msg.ID)
	return tea.Batch(m.setFocus(focusInput), m.afterScroll())
}

func (m *model) startEdit(msg core.Message) tea.Cmd {
	switch {
	case !fitsComposer(msg.Text):
		m.status = "the composer cannot hold this message as it is (line breaks, special characters), edit it in another Telegram app"
		return nil
	case m.saving[m.openID] > 0:
		m.status = savingNote
		return nil
	case m.attached():
		m.status = "send or remove the attachment first, an edit changes the text only"
		return nil
	case m.readingClipboard():
		// The image being read would attach to the edit.
		m.status = waitClipboard
		return nil
	case m.editing == msg.ID:
		return m.setFocus(focusInput) // keep the changes made so far
	}
	if m.editing == 0 && m.stash == nil {
		m.stash = &stash{m.input.Value(), m.replyTo}
	}
	m.replyTo, m.editing, m.editFrom = 0, msg.ID, msg.Text
	m.input.SetValue(msg.Text)
	m.input.CursorEnd()
	m.revealMessage(msg.ID)
	return tea.Batch(m.setFocus(focusInput), m.afterScroll())
}

// cancelDraft drops the reply or the edit. A reply keeps what was typed, an
// edit gives the composer back what it held before.
func (m *model) cancelDraft() {
	if m.editing == 0 {
		m.replyTo = 0
		return
	}
	m.editing = 0
	m.unstash()
}

// unstash puts back what the composer held before an edit.
func (m *model) unstash() {
	m.input.Reset()
	if m.stash != nil {
		m.input.SetValue(m.stash.text)
		m.input.CursorEnd()
		m.replyTo = m.stash.replyTo
	}
	m.stash = nil
}

// pickReply moves the reply to the message before (-1) or after (1) the one
// picked, as Telegram does with ctrl+up and ctrl+down. Going past the newest
// message drops the reply.
func (m *model) pickReply(delta int) tea.Cmd {
	h := m.histories[m.openID]
	if h == nil || m.editing != 0 {
		return nil // an edit is cancelled with esc, not by picking a reply
	}
	msgs := slices.DeleteFunc(slices.Clone(h.msgs), func(msg core.Message) bool { return msg.Service })
	i := slices.IndexFunc(msgs, func(msg core.Message) bool { return msg.ID == m.replyTo })
	if i < 0 {
		i = len(msgs)
	}
	i += delta
	switch {
	case i >= len(msgs):
		m.replyTo = 0
		return m.afterScroll()
	case i < 0:
		return nil // the oldest loaded message, page up loads more
	}
	return m.startReply(msgs[i])
}

// lastOwn is the newest message with text the account sent in the open chat.
func (m *model) lastOwn() (core.Message, bool) {
	if h := m.histories[m.openID]; h != nil {
		for i := len(h.msgs) - 1; i >= 0; i-- {
			if editable(h.msgs[i]) {
				return h.msgs[i], true
			}
		}
	}
	return core.Message{}, false
}

// message finds a loaded message of the open chat.
func (m *model) message(id int) (core.Message, bool) {
	if h := m.histories[m.openID]; h != nil {
		if i := slices.IndexFunc(h.msgs, func(msg core.Message) bool { return msg.ID == id }); i >= 0 {
			return h.msgs[i], true
		}
	}
	return core.Message{}, false
}

// openMenu opens the context menu of the message drawn at screen cell x, y,
// if there is one.
func (m *model) openMenu(x, y int) {
	msg, ok := m.messageAt(y)
	if !ok {
		return
	}
	var items []menuItem
	if !msg.Service {
		items = append(items, menuItem{"Reply", (*model).startReply})
	}
	if editable(msg) && fitsComposer(msg.Text) {
		items = append(items, menuItem{"Edit", (*model).startEdit})
	}
	if msg.Text != "" {
		items = append(items, menuItem{"Copy text", func(m *model, msg core.Message) tea.Cmd {
			return m.copy(msg.Text, "text")
		}})
	}
	items = append(items, menuItem{"Copy message ID", func(m *model, msg core.Message) tea.Cmd {
		return m.copy(strconv.Itoa(msg.ID), "message ID "+strconv.Itoa(msg.ID))
	}})
	// Open where the click was, moved in so the whole menu stays on screen.
	lw, _ := m.layout()
	if lw > 0 {
		lw++ // the border
	}
	x = max(lw, min(x, m.width-menuWidth(items)))
	y = max(0, min(y, m.height-1-len(items)))
	m.menu = &menu{msg: msg, items: items, x: x, y: y}
}

// editable reports whether the TUI offers to edit msg: the account sent it
// and it has text. Media without a caption, like a sticker, has none.
func editable(msg core.Message) bool { return msg.Out && !msg.Service && msg.Text != "" }

// fitsComposer reports whether the composer holds text unchanged. It is one
// line and drops what it cannot show (line breaks, tabs, U+FFFD), so editing
// such a message would rewrite it even when nothing was typed.
func fitsComposer(text string) bool {
	in := textinput.New()
	in.SetValue(text)
	return in.Value() == text
}

// runMenu runs the selected item on the message as it is now: it may have
// been edited since the menu opened.
func (m *model) runMenu() tea.Cmd {
	menu := m.menu
	m.menu = nil
	msg := menu.msg
	if current, ok := m.message(msg.ID); ok {
		msg = current
	}
	return menu.items[menu.sel].run(m, msg)
}

// copy puts s on the clipboard with OSC 52, which also works over SSH.
func (m *model) copy(s, what string) tea.Cmd {
	m.note = "copied " + what
	return tea.SetClipboard(s)
}

// pasteText handles text pasted into the composer, by the terminal or read
// from the clipboard. The path of an image, which is what dropping a file on
// the terminal pastes, attaches it. It never reads the clipboard itself.
func (m *model) pasteText(text string) tea.Cmd {
	if path, ok := imagePath(text); ok && m.editing == 0 {
		m.setAttach(core.SendRequest{File: path})
		return nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(tea.PasteMsg{Content: text})
	return cmd
}

// waitClipboard is the status shown when enter waits for a clipboard read.
const waitClipboard = "reading the clipboard, esc cancels"

func (m *model) readingClipboard() bool { return m.pasting != 0 && m.pasting == m.attachSeq }

func (m *model) setAttach(req core.SendRequest) {
	m.attach = req
	m.attachSeq++
}

// pasteImage reads the system clipboard for terminals that only paste text:
// its image, or else its text.
func (m *model) pasteImage() tea.Cmd {
	m.startRead()
	seq := m.attachSeq
	return func() tea.Msg {
		data, err := clipboard.Image(m.ctx)
		if err != nil {
			text, _ := textclip.ReadAll()
			return clipboardMsg{seq: seq, text: text, err: err}
		}
		return clipboardMsg{seq: seq, png: data}
	}
}

// keepView applies change, which redraws message msgID of the open chat, so
// that what the user reads does not move. The conversation is anchored at
// its bottom, so lines that grow inside or below the view would push it up
// unless the scroll offset grows with them.
func (m *model) keepView(msgID int, change func()) {
	h := m.histories[m.openID]
	rw := m.conversationWidth()
	if m.scroll == 0 || h == nil || rw == 0 {
		change()
		return
	}
	before := m.messageLines(h, rw)
	top := len(before) - m.scroll - m.messageRows()
	end := -1 // the line after the message, where lines are added
	for i, l := range before {
		if l.msg >= 0 && h.msgs[l.msg].ID == msgID {
			end = i + 1
		}
	}
	change()
	if end > top {
		m.scroll += len(m.messageLines(h, rw)) - len(before)
	}
}

// imagePath reports whether pasted text is the absolute path of one image
// file. File managers and terminals paste it as a file:// URL, quoted, or with
// spaces escaped by backslashes, so each reading is tried. A bare file name is
// text: it only names a file by chance.
func imagePath(text string) (string, bool) {
	text = strings.TrimSpace(text)
	candidates := []string{text, strings.Trim(text, `'"`), strings.ReplaceAll(text, `\`, "")}
	if u, err := url.Parse(text); err == nil && u.Scheme == "file" && (u.Host == "" || u.Host == "localhost") {
		path := u.Path
		if runtime.GOOS == "windows" && len(path) > 2 && path[0] == '/' && path[2] == ':' {
			path = filepath.FromSlash(path[1:]) // file:///C:/x.png
		}
		candidates = append(candidates, path)
	}
	for _, path := range candidates {
		if !filepath.IsAbs(path) || !core.IsPhotoFile(path) {
			continue
		}
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path, true
		}
	}
	return "", false
}

func (m *model) setFocus(f focus) tea.Cmd {
	m.focus = f
	m.input.Blur()
	m.filter.Blur()
	switch f {
	case focusInput:
		// A narrow terminal shows the conversation again: re-clamp what
		// changed while it was hidden.
		return tea.Batch(m.input.Focus(), m.afterScroll())
	case focusFilter:
		return m.filter.Focus()
	}
	return nil
}

// visible is the chat list after applying the filter.
func (m *model) visible() []*core.Chat {
	needle := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	out := make([]*core.Chat, 0, len(m.chats))
	for i := range m.chats {
		c := &m.chats[i]
		if needle == "" || strings.Contains(strings.ToLower(c.Title), needle) || strings.Contains(strings.ToLower(c.Username), needle) {
			out = append(out, c)
		}
	}
	return out
}

func (m *model) chat(id int64) *core.Chat {
	for i := range m.chats {
		if m.chats[i].ID == id {
			return &m.chats[i]
		}
	}
	return nil
}

// afterMove clamps the selection, keeps it on screen and requests the
// avatars of the chats that scrolled into view.
func (m *model) afterMove() tea.Cmd {
	chats := m.visible()
	m.sel = max(0, min(m.sel, len(chats)-1))
	rows := m.listRows()
	m.top = max(0, min(m.top, m.sel, len(chats)-rows))
	if m.sel >= m.top+rows {
		m.top = m.sel - rows + 1
	}
	return m.loadAvatars()
}

// loadAvatars requests the avatars of the chats in view, while the chat list
// shows.
func (m *model) loadAvatars() tea.Cmd {
	if lw, _ := m.layout(); !m.graphics || lw == 0 {
		return nil
	}
	chats := m.visible()
	var cmds []tea.Cmd
	for _, c := range chats[min(m.top, len(chats)):min(len(chats), m.top+m.listRows())] {
		key := imageKey{chat: c.ID}
		if _, seen := m.images[key]; seen {
			continue
		}
		seq := m.loading(key)
		chat := *c
		if chat.PhotoID == 0 {
			// No photo: the initials badge, drawn as an image so they center.
			bg, fg := m.st.peer(chat.ID), m.st.theme.Background
			cmds = append(cmds, func() tea.Msg {
				png, err := termimg.Initials(initials(chat.Title), bg, fg, avatarPx)
				if err != nil {
					return imageMsg{key: key, seq: seq}
				}
				return imageMsg{key: key, seq: seq, frames: [][]byte{png}, cols: avatarCols, rows: avatarRows}
			})
			continue
		}
		cmds = append(cmds, func() tea.Msg {
			data, err := m.be.Avatar(m.ctx, chat)
			if err != nil {
				return imageMsg{key: key, seq: seq}
			}
			png, err := termimg.Avatar(data, avatarPx)
			if err != nil {
				return imageMsg{key: key, seq: seq}
			}
			return imageMsg{key: key, seq: seq, frames: [][]byte{png}, cols: avatarCols, rows: avatarRows}
		})
	}
	return tea.Batch(cmds...)
}

// pictureBox is the most cells a picture of this kind may cover.
func pictureBox(kind core.PictureKind) (cols, rows int) {
	if kind == core.PictureSticker {
		return stickerCols, stickerRows
	}
	return photoCols, photoRows
}

// loadPictures requests the pictures of the messages on screen in the open
// chat, sized to fit the conversation pane as it is now. A clip that ffmpeg
// can decode plays, otherwise its still is shown.
func (m *model) loadPictures() tea.Cmd {
	h := m.histories[m.openID]
	_, rw := m.layout()
	if !m.graphics || h == nil || rw == 0 {
		return nil
	}
	lines := m.messageLines(h, rw)
	start, end, _, _ := m.shown(len(lines))
	var cmds []tea.Cmd
	for _, l := range lines[start:end] {
		if l.msg < 0 || h.msgs[l.msg].Picture == core.NoPicture {
			continue
		}
		msg := h.msgs[l.msg]
		boxCols, boxRows := pictureBox(msg.Picture)
		maxCols := min(boxCols, rw-photoIndent-1)
		if maxCols < 1 {
			continue // no room for a picture, its label is shown
		}
		key := imageKey{msg.ChatID, msg.ID}
		// A picture sized for a wider pane is fetched again to fit, and
		// the old one freed.
		img, seen := m.images[key]
		if seen && img.cols <= maxCols {
			continue
		}
		if seen {
			m.drop(key)
		}
		seq := m.loading(key)
		view, jobs := m.view, m.jobs
		cmds = append(cmds, func() tea.Msg {
			select {
			case jobs <- struct{}{}:
				defer func() { <-jobs }()
			case <-view.Done():
				return imageMsg{key: key, seq: seq, retry: true}
			}
			pic, err := m.be.Picture(view, key.chat, key.msg)
			if err != nil {
				return imageMsg{key: key, seq: seq, retry: view.Err() != nil}
			}
			png, cols, rows, err := termimg.Fit(pic.Still, maxCols, boxRows)
			if err != nil {
				return imageMsg{key: key, seq: seq}
			}
			out := imageMsg{key: key, seq: seq, frames: [][]byte{png}, cols: cols, rows: rows}
			if pic.Clip != nil {
				if frames, gap, err := termimg.Frames(view, pic.Clip, cols, rows); err == nil {
					out.frames, out.gap = frames, gap
				}
			}
			if view.Err() != nil {
				return imageMsg{key: key, seq: seq, retry: true}
			}
			return out
		})
	}
	return tea.Batch(cmds...)
}

// loading marks the picture of key as loading and returns the number of
// this load.
func (m *model) loading(key imageKey) int {
	m.loadSeq++
	m.images[key] = shown{seq: m.loadSeq}
	return m.loadSeq
}

// nextImage picks the ID of a new picture: the next one in turn that no
// picture holds and that was not freed in the last second, as its delete may
// still be on its way. With none left, the oldest picture off screen gives
// its ID up: the upload replaces it, no delete needed.
func (m *model) nextImage() int {
	held := maps.Clone(m.uploading)
	for _, img := range m.images {
		held[img.id] = true
	}
	cooling := func(id int) bool { return time.Since(m.freedAt[id]) < time.Second }
	for i := range termimg.MaxID {
		if id := (m.lastImage+i)%termimg.MaxID + 1; !held[id] && !cooling(id) {
			m.lastImage = id
			return id
		}
	}
	screen := m.onScreen()
	for _, s := range m.stored {
		if img, ok := m.images[s.key]; ok && img.id == s.id && !screen[s.key] {
			m.forget(s.key)
			m.lastImage = s.id
			return s.id
		}
	}
	// Every ID is on screen: the next in turn is replaced.
	m.lastImage = m.lastImage%termimg.MaxID + 1
	for key, img := range m.images {
		if img.id == m.lastImage {
			m.forget(key)
		}
	}
	return m.lastImage
}

// forget drops the picture of key from the count, for when the terminal
// image is gone or about to be replaced. Its rows leave the conversation
// without moving what the user reads.
func (m *model) forget(key imageKey) {
	change := func() {
		m.imageBytes -= m.images[key].bytes
		delete(m.images, key)
	}
	if key.chat == m.openID && key.msg != 0 {
		m.keepView(key.msg, change)
		return
	}
	change()
}

// drop forgets the picture of key and frees its image in the terminal. It
// loads again if it is seen again.
func (m *model) drop(key imageKey) {
	if id := m.images[key].id; id != 0 {
		m.free = append(m.free, id)
	}
	m.forget(key)
}

// evict frees the oldest pictures while the terminal holds more than
// imageBudget, except those on screen: freeing them would only load them
// again. They load again when they come back into view.
func (m *model) evict() {
	if m.imageBytes <= imageBudget && len(m.stored) <= 2*termimg.MaxID {
		return
	}
	var screen map[imageKey]bool
	if m.imageBytes > imageBudget {
		screen = m.onScreen()
	}
	var keep []stored
	for _, s := range m.stored {
		img, ok := m.images[s.key]
		switch {
		case !ok || img.id != s.id: // replaced or dropped already
		case m.imageBytes > imageBudget && !screen[s.key]:
			m.drop(s.key)
		default:
			keep = append(keep, s)
		}
	}
	m.stored = keep
}

// onScreen is the set of pictures drawn right now: avatars of the chats in
// view and the media of the messages in view.
func (m *model) onScreen() map[imageKey]bool {
	keys := map[imageKey]bool{}
	lw, rw := m.layout()
	if lw > 0 {
		chats := m.visible()
		for _, c := range chats[min(m.top, len(chats)):min(len(chats), m.top+m.listRows())] {
			keys[imageKey{chat: c.ID}] = true
		}
	}
	h := m.histories[m.openID]
	if h != nil && rw > 0 {
		lines := m.messageLines(h, rw)
		start, end, _, _ := m.shown(len(lines))
		for _, l := range lines[start:end] {
			if l.msg >= 0 {
				keys[imageKey{h.msgs[l.msg].ChatID, h.msgs[l.msg].ID}] = true
			}
		}
	}
	return keys
}

// afterScroll clamps the message scroll offset and loads older history when
// the top of what is loaded comes into view.
func (m *model) afterScroll() tea.Cmd {
	h := m.histories[m.openID]
	if h == nil {
		m.scroll = 0
		return nil
	}
	_, rw := m.layout()
	if rw == 0 {
		return nil // the conversation is hidden, its width unknown
	}
	limit := max(0, len(m.messageLines(h, rw))-m.messageRows())
	m.scroll = max(0, min(m.scroll, limit))
	if m.scroll < limit || h.done || h.loading || len(h.msgs) == 0 {
		return nil
	}
	h.loading = true
	chatID, before := m.openID, h.msgs[0].ID
	return func() tea.Msg {
		msgs, err := m.be.History(m.ctx, chatID, core.HistoryQuery{Limit: historyPage, BeforeID: before})
		return historyMsg{chatID, msgs, true, err}
	}
}

func (m *model) openSelected() tea.Cmd {
	chats := m.visible()
	if m.sel >= len(chats) {
		return nil
	}
	chatID := chats[m.sel].ID
	if chatID != m.openID {
		m.leave() // the pictures still loading there are not needed now
		m.view, m.leave = context.WithCancel(m.ctx)
	}
	m.openID, m.scroll = chatID, 0
	m.replyTo, m.editing, m.stash = 0, 0, nil
	m.input.Reset()
	m.setAttach(core.SendRequest{}) // it was meant for the previous chat
	cmds := []tea.Cmd{m.setFocus(focusInput)}
	if h := m.histories[chatID]; h == nil {
		m.histories[chatID] = &history{loading: true}
		cmds = append(cmds, func() tea.Msg {
			msgs, err := m.be.History(m.ctx, chatID, core.HistoryQuery{Limit: historyPage})
			return historyMsg{chatID, msgs, false, err}
		})
	} else {
		cmds = append(cmds, m.markRead(chatID))
	}
	return tea.Batch(cmds...)
}

// markRead clears the unread counter of a chat, locally and on the server.
func (m *model) markRead(chatID int64) tea.Cmd {
	h, c := m.histories[chatID], m.chat(chatID)
	if c != nil {
		c.Unread = 0
	}
	if h == nil || len(h.msgs) == 0 {
		return nil
	}
	maxID := h.msgs[len(h.msgs)-1].ID
	return func() tea.Msg {
		// Opening a chat marks it read as a courtesy. When the account's
		// guardrails forbid that, there is nothing to report.
		if err := m.be.MarkRead(m.ctx, chatID, maxID); err != nil && !errors.As(err, new(*core.RefusedError)) {
			return errMsg{err}
		}
		return nil
	}
}

// addMessage records a new or edited message in the history and in the chat
// list. chat describes the conversation for chats not listed yet. An edit of
// a message that is not loaded stays out of the history: it is older than
// the loaded page and must not show up as the newest.
func (m *model) addMessage(msg core.Message, chat *core.Chat, edit bool) {
	if h := m.histories[msg.ChatID]; h != nil && h.loaded {
		if i := slices.IndexFunc(h.msgs, func(o core.Message) bool { return o.ID == msg.ID }); i >= 0 {
			h.msgs[i] = msg
			m.drop(imageKey{msg.ChatID, msg.ID}) // an edit can replace the media
		} else if !edit && (h.done || len(h.msgs) == 0 || msg.ID > h.msgs[0].ID) {
			// By ID, which is the order Telegram gave them, whatever order
			// they arrive in. One older than the whole loaded page is left
			// to the older pages while there are any: added first, it would
			// make them skip what lies between.
			at, _ := slices.BinarySearchFunc(h.msgs, msg.ID, func(o core.Message, id int) int { return cmp.Compare(o.ID, id) })
			h.msgs = slices.Insert(h.msgs, at, msg)
			if msg.ChatID == m.openID && m.scroll > 0 {
				m.scroll = 0
			}
		}
	}

	var selected int64
	if chats := m.visible(); m.sel < len(chats) {
		selected = chats[m.sel].ID
	}

	i := slices.IndexFunc(m.chats, func(c core.Chat) bool { return c.ID == msg.ChatID })
	if i < 0 {
		if chat == nil {
			return
		}
		m.chats = append(m.chats, *chat)
		i = len(m.chats) - 1
	}
	c := m.chats[i]
	if c.Last == nil || msg.ID >= c.Last.ID {
		isNew := c.Last == nil || msg.ID > c.Last.ID
		c.Last = &msg
		if isNew && !msg.Out && msg.ChatID != m.openID {
			c.Unread++
		}
		// Bump the chat to the top, below pinned chats unless it is pinned.
		m.chats = slices.Delete(m.chats, i, i+1)
		at := 0
		if !c.Pinned {
			for at < len(m.chats) && m.chats[at].Pinned {
				at++
			}
		}
		m.chats = slices.Insert(m.chats, at, c)
	}

	// Keep the cursor on the chat it was on.
	if j := slices.IndexFunc(m.visible(), func(c *core.Chat) bool { return c.ID == selected }); j >= 0 {
		m.sel = j
	}
}

func (m *model) loadChats(after *core.Chat) tea.Cmd {
	var cursor *core.Chat
	if after != nil {
		c := *after
		cursor = &c
	}
	return func() tea.Msg {
		chats, err := m.be.Chats(m.ctx, core.ChatQuery{Limit: chatPage, After: cursor})
		return chatsMsg{chats, err}
	}
}
