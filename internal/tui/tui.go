// Package tui is the interactive client: chat list on the left, the open
// conversation and a composer on the right. It only talks to a core.Backend,
// so it runs unchanged against Telegram and against the demo data.
package tui

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

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
	// next message. Its Text is unused.
	attach core.SendRequest

	// images holds the pictures uploaded to the terminal. An entry with ID
	// 0 is loading, or failed and shows as text.
	images    map[imageKey]shown
	lastImage int

	status string
}

// imageKey names a picture: the photo of a message, or the avatar of a chat
// when msg is 0.
type imageKey struct {
	chat int64
	msg  int
}

// shown is a picture uploaded to the terminal as image id, drawn over a
// cols x rows cell area.
type shown struct{ id, cols, rows int }

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
	sentMsg struct {
		chatID int64
		req    core.SendRequest
		msg    core.Message
		err    error
	}
	// imageMsg carries a picture ready to upload, png is nil when it could
	// not be loaded.
	imageMsg struct {
		key        imageKey
		png        []byte
		cols, rows int
	}
	// uploadedMsg follows the upload of a picture to the terminal.
	uploadedMsg struct {
		key imageKey
		img shown
	}
	// clipboardMsg carries an image read from the system clipboard for the
	// composer of chatID.
	clipboardMsg struct {
		chatID int64
		png    []byte
		err    error
	}
	errMsg   struct{ err error }
	eventMsg core.Event
)

// Run starts the TUI and blocks until the user quits. account, when not
// empty, is shown so it is clear whose chats these are. theme names one of
// Themes, "" for the default.
func Run(ctx context.Context, be core.Backend, version, account, theme string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	m := newModel(ctx, be, theme)
	m.version, m.account, m.graphics = version, account, termimg.Supported()
	p := tea.NewProgram(m, tea.WithContext(ctx))
	go func() {
		if err := be.Watch(ctx, func(ev core.Event) { p.Send(eventMsg(ev)) }); err != nil && ctx.Err() == nil {
			p.Send(errMsg{err})
		}
	}()
	_, err := p.Run()

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
	m := &model{ctx: ctx, be: be, histories: map[int64]*history{}, images: map[imageKey]shown{}}
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

func (m *model) Init() tea.Cmd { return m.loadChats(nil) }

// Update handles a message, then fetches the photos that came into view,
// whatever moved them there.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := m.update(msg)
	return m, tea.Batch(cmd, m.loadPhotos())
}

func (m *model) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m.afterMove()

	case tea.KeyPressMsg:
		return m.key(msg)

	case tea.MouseWheelMsg:
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
		if msg.Button != tea.MouseLeft || msg.Y < 1 || msg.Y >= m.height-1 {
			return nil
		}
		if msg.X >= lw {
			return m.setFocus(focusInput)
		}
		if i := m.top + (msg.Y-1)/2; i < len(m.visible()) {
			m.sel = i
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
			// Give the message back, so it is not lost, unless something
			// else was written since.
			if msg.chatID == m.openID && m.input.Value() == "" && !m.attached() {
				m.input.SetValue(msg.req.Text)
				m.attach, m.attach.Text = msg.req, ""
			}
			return nil
		}
		m.addMessage(msg.msg, nil)
		m.scroll = 0
		return nil

	case eventMsg:
		ev := core.Event(msg)
		if ev.Kind == core.EventReady {
			return nil
		}
		m.addMessage(ev.Message, &ev.Chat)
		cmds := []tea.Cmd{m.afterMove()}
		if ev.Message.ChatID == m.openID && !ev.Message.Out {
			cmds = append(cmds, m.markRead(m.openID))
		}
		return tea.Batch(cmds...)

	case imageMsg:
		if msg.png == nil {
			return nil // stays 0, initials or "[photo]" are shown
		}
		m.lastImage = m.lastImage%termimg.MaxID + 1
		for key, img := range m.images {
			if img.id == m.lastImage {
				delete(m.images, key) // ID is being reused, it reloads if seen again
			}
		}
		// The picture is shown only once it reached the terminal: placeholder
		// cells drawn before their image may stay blank.
		img := shown{m.lastImage, msg.cols, msg.rows}
		return tea.Sequence(
			tea.Raw(termimg.Transmit(img.id, img.cols, img.rows, msg.png)),
			func() tea.Msg { return uploadedMsg{msg.key, img} },
		)

	case uploadedMsg:
		m.keepView(msg.key.msg, func() { m.images[msg.key] = msg.img })
		return nil

	case clipboardMsg:
		switch {
		case msg.chatID != m.openID:
			return nil // the image was meant for another chat
		case msg.err == nil:
			m.attach = core.SendRequest{Photo: msg.png}
			return nil
		case !errors.Is(msg.err, clipboard.ErrNoImage):
			m.status = msg.err.Error()
		}
		return textinput.Paste // no image, ctrl+v pastes text as before

	case tea.PasteMsg:
		if m.focus == focusInput {
			return m.paste(msg.Content)
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
	m.status = ""
	if key == "ctrl+c" {
		return tea.Quit
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
			return m.setFocus(focusChats)
		case "pgup":
			m.scroll += m.messageRows() / 2
			return m.afterScroll()
		case "pgdown":
			m.scroll -= m.messageRows() / 2
			return m.afterScroll()
		case "ctrl+v":
			return m.pasteImage(m.openID)
		case "backspace":
			if m.input.Value() == "" && m.attached() {
				m.attach = core.SendRequest{}
				return nil
			}
		case "enter":
			req := m.attach
			req.Text = strings.TrimSpace(m.input.Value())
			if req.Text == "" && !m.attached() {
				return nil
			}
			m.input.Reset()
			m.attach = core.SendRequest{}
			chatID := m.openID
			return func() tea.Msg {
				sent, err := m.be.Send(m.ctx, chatID, req)
				return sentMsg{chatID, req, sent, err}
			}
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return cmd
	}
}

func (m *model) attached() bool { return m.attach.File != "" || m.attach.Photo != nil }

// paste handles text pasted into the composer. The path of an image, which
// is what dropping a file on the terminal pastes, attaches it. An empty paste
// is what some terminals send when the clipboard holds only an image.
func (m *model) paste(text string) tea.Cmd {
	if strings.TrimSpace(text) == "" {
		return m.pasteImage(m.openID)
	}
	if path, ok := imagePath(text); ok {
		m.attach = core.SendRequest{File: path}
		return nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(tea.PasteMsg{Content: text})
	return cmd
}

// pasteImage reads an image from the system clipboard into the composer of
// chatID, for terminals that only paste text.
func (m *model) pasteImage(chatID int64) tea.Cmd {
	return func() tea.Msg {
		data, err := clipboard.Image(m.ctx)
		return clipboardMsg{chatID, data, err}
	}
}

// keepView applies change, which redraws message msgID of the open chat, so
// that what the user reads does not move. The conversation is anchored at
// its bottom, so lines that grow inside or below the view would push it up
// unless the scroll offset grows with them.
func (m *model) keepView(msgID int, change func()) {
	h := m.histories[m.openID]
	_, rw := m.layout()
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
	if u, err := url.Parse(text); err == nil && u.Scheme == "file" {
		candidates = append(candidates, u.Path)
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
		return m.input.Focus()
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
	if !m.graphics {
		return nil
	}
	var cmds []tea.Cmd
	for _, c := range chats[m.top:min(len(chats), m.top+rows)] {
		key := imageKey{chat: c.ID}
		if _, seen := m.images[key]; seen || c.PhotoID == 0 {
			continue
		}
		m.images[key] = shown{}
		chat := *c
		cmds = append(cmds, func() tea.Msg {
			data, err := m.be.Avatar(m.ctx, chat)
			if err != nil {
				return imageMsg{key: key}
			}
			png, _ := termimg.Avatar(data, avatarPx)
			return imageMsg{key, png, avatarCols, avatarRows}
		})
	}
	return tea.Batch(cmds...)
}

// loadPhotos requests the photos of the messages on screen in the open chat,
// sized to fit the conversation pane as it is now.
func (m *model) loadPhotos() tea.Cmd {
	h := m.histories[m.openID]
	_, rw := m.layout()
	if !m.graphics || h == nil || rw == 0 {
		return nil
	}
	lines := m.messageLines(h, rw)
	end := max(0, len(lines)-m.scroll)
	maxCols := min(photoCols, rw-photoIndent-1)
	if maxCols < 1 {
		return nil // no room for a photo, its label is shown
	}
	var cmds []tea.Cmd
	for _, l := range lines[max(0, end-m.messageRows()):end] {
		if l.msg < 0 || h.msgs[l.msg].Media != core.MediaPhoto {
			continue
		}
		msg := h.msgs[l.msg]
		key := imageKey{msg.ChatID, msg.ID}
		// A photo sized for a wider pane is fetched again to fit.
		if img, seen := m.images[key]; seen && img.cols <= maxCols {
			continue
		}
		m.images[key] = shown{}
		cmds = append(cmds, func() tea.Msg {
			data, err := m.be.Photo(m.ctx, key.chat, key.msg)
			if err != nil {
				return imageMsg{key: key}
			}
			png, cols, rows, err := termimg.Fit(data, maxCols, photoRows)
			if err != nil {
				return imageMsg{key: key}
			}
			return imageMsg{key, png, cols, rows}
		})
	}
	return tea.Batch(cmds...)
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
	m.openID, m.scroll = chatID, 0
	m.input.Reset()
	m.attach = core.SendRequest{} // it was meant for the previous chat
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
// list. chat describes the conversation for chats not listed yet.
func (m *model) addMessage(msg core.Message, chat *core.Chat) {
	if h := m.histories[msg.ChatID]; h != nil && h.loaded {
		if i := slices.IndexFunc(h.msgs, func(o core.Message) bool { return o.ID == msg.ID }); i >= 0 {
			h.msgs[i] = msg
			delete(m.images, imageKey{msg.ChatID, msg.ID}) // an edit can replace the photo
		} else {
			h.msgs = append(h.msgs, msg)
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
