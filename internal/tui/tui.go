// Package tui is the interactive client: chat list on the left, the open
// conversation and a composer on the right. It only talks to a core.Backend,
// so it runs unchanged against Telegram and against the demo data.
package tui

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

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
	images  bool

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

	// avatars maps chat ID to an uploaded image ID, 0 while it is loading.
	avatars   map[int64]int
	lastImage int

	status string
}

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
		msg core.Message
		err error
	}
	avatarMsg struct {
		chatID int64
		png    []byte
	}
	errMsg   struct{ err error }
	eventMsg core.Event
)

// Run starts the TUI and blocks until the user quits.
func Run(ctx context.Context, be core.Backend, version string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	m := &model{
		ctx: ctx, be: be, version: version, images: termimg.Supported(),
		histories: map[int64]*history{}, avatars: map[int64]int{},
		input: newInput("> "), filter: newInput("/ "),
	}
	p := tea.NewProgram(m, tea.WithContext(ctx))
	go func() {
		if err := be.Watch(ctx, func(ev core.Event) { p.Send(eventMsg(ev)) }); err != nil && ctx.Err() == nil {
			p.Send(errMsg{err})
		}
	}()
	_, err := p.Run()

	if ids := slices.Collect(func(yield func(int) bool) {
		for _, id := range m.avatars {
			if id != 0 && !yield(id) {
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

func newInput(prompt string) textinput.Model {
	in := textinput.New()
	in.Prompt = prompt
	in.SetVirtualCursor(false)
	styles := in.Styles()
	styles.Focused.Prompt = accent
	styles.Blurred.Prompt = dim
	in.SetStyles(styles)
	return in
}

func (m *model) Init() tea.Cmd { return m.loadChats(nil) }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, m.afterMove()

	case tea.KeyPressMsg:
		return m, m.key(msg)

	case tea.MouseWheelMsg:
		delta := 1
		if msg.Button == tea.MouseWheelUp {
			delta = -1
		}
		if lw, _ := m.layout(); msg.X < lw {
			m.sel += delta
			return m, m.afterMove()
		}
		m.scroll -= delta * 3
		return m, m.afterScroll()

	case tea.MouseClickMsg:
		lw, _ := m.layout()
		if msg.Button != tea.MouseLeft || msg.Y < 1 || msg.Y >= m.height-1 {
			return m, nil
		}
		if msg.X >= lw {
			return m, m.setFocus(focusInput)
		}
		if i := m.top + (msg.Y-1)/2; i < len(m.visible()) {
			m.sel = i
			return m, m.openSelected()
		}
		return m, nil

	case chatsMsg:
		if msg.err != nil {
			m.status = msg.err.Error()
			return m, nil
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
		return m, tea.Batch(cmds...)

	case historyMsg:
		h := m.histories[msg.chatID]
		h.loading = false
		if msg.err != nil {
			m.status = msg.err.Error()
			return m, nil
		}
		h.loaded = true
		if msg.older {
			h.done = len(msg.msgs) < historyPage
			h.msgs = append(msg.msgs, h.msgs...)
			return m, nil
		}
		h.done = len(msg.msgs) < historyPage
		h.msgs = msg.msgs
		return m, m.markRead(msg.chatID)

	case sentMsg:
		if msg.err != nil {
			m.status = "send failed: " + msg.err.Error()
			return m, nil
		}
		m.addMessage(msg.msg, nil)
		m.scroll = 0
		return m, nil

	case eventMsg:
		ev := core.Event(msg)
		if ev.Kind == core.EventReady {
			return m, nil
		}
		m.addMessage(ev.Message, &ev.Chat)
		cmds := []tea.Cmd{m.afterMove()}
		if ev.Message.ChatID == m.openID && !ev.Message.Out {
			cmds = append(cmds, m.markRead(m.openID))
		}
		return m, tea.Batch(cmds...)

	case avatarMsg:
		if msg.png == nil {
			return m, nil // stays 0, initials are shown
		}
		m.lastImage = m.lastImage%termimg.MaxID + 1
		for chatID, id := range m.avatars {
			if id == m.lastImage {
				delete(m.avatars, chatID) // ID is being reused
			}
		}
		m.avatars[msg.chatID] = m.lastImage
		return m, tea.Raw(termimg.Transmit(m.lastImage, avatarCols, avatarRows, msg.png))

	case errMsg:
		m.status = msg.err.Error()
		return m, nil
	}

	// Everything else (paste, cursor blink) belongs to the focused input.
	var cmd tea.Cmd
	switch m.focus {
	case focusInput:
		m.input, cmd = m.input.Update(msg)
	case focusFilter:
		m.filter, cmd = m.filter.Update(msg)
	}
	return m, cmd
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
		case "enter":
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return nil
			}
			m.input.Reset()
			chatID := m.openID
			return func() tea.Msg {
				sent, err := m.be.Send(m.ctx, chatID, core.SendRequest{Text: text})
				return sentMsg{sent, err}
			}
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return cmd
	}
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
	if !m.images {
		return nil
	}
	var cmds []tea.Cmd
	for _, c := range chats[m.top:min(len(chats), m.top+rows)] {
		if _, seen := m.avatars[c.ID]; seen || c.PhotoID == 0 {
			continue
		}
		m.avatars[c.ID] = 0
		chat := *c
		cmds = append(cmds, func() tea.Msg {
			data, err := m.be.Avatar(m.ctx, chat)
			if err != nil {
				return avatarMsg{chatID: chat.ID}
			}
			png, _ := termimg.Avatar(data, avatarPx)
			return avatarMsg{chat.ID, png}
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
		if err := m.be.MarkRead(m.ctx, chatID, maxID); err != nil {
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
