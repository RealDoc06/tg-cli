package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/RealDoc06/tg-cli/internal/core"
	"github.com/RealDoc06/tg-cli/internal/termimg"
)

// layout returns the widths of the chat list and of the conversation pane.
// Narrow terminals show only the pane that has focus.
func (m *model) layout() (left, right int) {
	if m.width < narrowWidth {
		if m.focus == focusInput {
			return 0, m.width
		}
		return m.width, 0
	}
	left = max(30, min(42, m.width/3))
	return left, m.width - left - 1
}

// A chat takes two rows in the list, and chatGap blank ones set it apart
// from the next: chat k starts on row 1+chatStride*k, below the list's title.
const (
	chatGap    = 1
	chatStride = 2 + chatGap
)

// chatsDrawn is how many chats fit in the list, possibly none. listRows is
// the same at least one, for paging and keeping the selection in view.
func (m *model) chatsDrawn() int { return max(0, (m.height-1)/chatStride) }
func (m *model) listRows() int   { return max(1, m.chatsDrawn()) }

// The conversation pane is, top to bottom: the title and a gap, the messages,
// a gap, the reply or edit being written when there is one, and the composer.
const messagesTop = 2

func (m *model) draftRows() int {
	if m.replyTo != 0 || m.editing != 0 {
		return 1
	}
	return 0
}

// messageRows is how many lines of conversation fit.
func (m *model) messageRows() int { return max(1, m.height-1-messagesTop-m.draftRows()-2) }

// shown picks the n lines of conversation that are on screen: lines[start:end],
// drawn below pad empty rows. overflow is how many rows at the top of the pane
// a tiny terminal cuts off.
func (m *model) shown(n int) (start, end, pad, overflow int) {
	view := m.messageRows()
	// Clamped here as well: a message that got shorter while the pane was
	// scrolled must not leave it blank.
	end = max(0, n-min(m.scroll, max(0, n-view)))
	start = max(0, end-view)
	overflow = max(0, messagesTop+view+m.draftRows()+2-(m.height-1))
	return start, end, view - (end - start), overflow
}

// messageAt is the message drawn on screen row y.
func (m *model) messageAt(y int) (core.Message, bool) {
	h := m.histories[m.openID]
	_, rw := m.layout()
	if h == nil || !h.loaded || rw == 0 {
		return core.Message{}, false
	}
	_, ids := m.messageLines(h, rw)
	start, end, pad, overflow := m.shown(len(ids))
	i := start + y - (messagesTop - overflow) - pad
	if i < start || i >= end || ids[i] == 0 {
		return core.Message{}, false
	}
	return m.message(ids[i])
}

// revealMessage scrolls the conversation so message id is on screen.
func (m *model) revealMessage(id int) {
	h := m.histories[m.openID]
	_, rw := m.layout()
	if h == nil || rw == 0 {
		return
	}
	_, ids := m.messageLines(h, rw)
	first, last := slices.Index(ids, id), -1
	for i, line := range ids {
		if line == id {
			last = i
		}
	}
	if first < 0 {
		return
	}
	if end := len(ids) - m.scroll; last >= end {
		m.scroll = len(ids) - 1 - last
	}
	if start := len(ids) - m.scroll - m.messageRows(); first < start {
		m.scroll = len(ids) - first - m.messageRows()
	}
	m.scroll = max(0, m.scroll)
}

func (m *model) View() tea.View {
	var v tea.View
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "tg"
	v.BackgroundColor = m.st.theme.Background
	v.ForegroundColor = m.st.theme.Text
	if m.width == 0 {
		return v
	}

	lw, rw := m.layout()
	body := m.height - 1
	var left, right []string
	if lw > 0 {
		left = m.chatList(lw, body)
	}
	if rw > 0 {
		right = m.conversation(rw, body)
	}

	rows := make([]string, 0, m.height)
	for i := range body {
		switch {
		case lw > 0 && rw > 0:
			rows = append(rows, left[i]+m.st.border.Render("│")+right[i])
		case lw > 0:
			rows = append(rows, left[i])
		default:
			rows = append(rows, right[i])
		}
	}
	rows = append(rows, m.statusLine())
	if m.menu != nil {
		w := menuWidth(m.menu.items)
		for i, item := range m.menu.items {
			style := m.st.menu
			if i == m.menu.sel {
				style = m.st.menuSel
			}
			if y := m.menu.y + i; y < len(rows) {
				rows[y] = overlay(rows[y], style.Render(fit(" "+item.label, w)), m.menu.x, m.width)
			}
		}
	}
	v.SetContent(strings.Join(rows, "\n"))

	// The terminal's own cursor marks the focused input, so nothing blinks
	// through repaints.
	switch m.focus {
	case focusInput:
		if c := m.input.Cursor(); c != nil && rw > 0 {
			c.X += m.width - rw + 1
			c.Y = body - 1
			c.Blink = false
			v.Cursor = c
		}
	case focusFilter:
		if c := m.filter.Cursor(); c != nil {
			c.X++
			c.Blink = false
			v.Cursor = c
		}
	}
	return v
}

// chatList renders the filter line followed by two lines per chat.
func (m *model) chatList(w, h int) []string {
	rows := make([]string, 0, h)
	switch {
	case m.focus == focusFilter || m.filter.Value() != "":
		m.filter.SetWidth(w - 4)
		rows = append(rows, fit(" "+m.filter.View(), w))
	default:
		rows = append(rows, fit(" "+m.st.accent.Bold(true).Render("Chats"), w))
	}

	chats := m.visible()
	if len(chats) == 0 && h > 1 {
		hint := "loading"
		if m.chatsDone {
			hint = "no chats"
		}
		rows = append(rows, fit(" "+m.st.muted.Render(hint), w))
	}
	for i := m.top; i < len(chats); i++ {
		gap := min(i-m.top, 1) * chatGap // none above the first
		if len(rows)+gap+2 > h {
			break
		}
		for range gap {
			rows = append(rows, strings.Repeat(" ", w))
		}
		rows = append(rows, m.chatRow(chats[i], i == m.sel, w)...)
	}
	for len(rows) < h {
		rows = append(rows, strings.Repeat(" ", w))
	}
	return rows[:h]
}

func (m *model) chatRow(c *core.Chat, selected bool, w int) []string {
	bar := " "
	if selected {
		bar = m.st.muted.Render("▌")
		if m.focus != focusInput {
			bar = m.st.accent.Render("▌")
		}
	}

	title := c.Title
	switch {
	case c.ID == m.openID:
		title = m.st.accent.Bold(true).Render(title)
	case selected || c.Unread > 0:
		title = m.st.bold.Render(title)
	}

	var stamp, preview, badge string
	if c.Last != nil {
		stamp = m.st.muted.Render(shortTime(c.Last.Date))
		preview = c.Last.Preview()
		switch {
		case c.Last.Out:
			preview = "You: " + preview
		case c.Kind == core.KindGroup && !c.Last.Service:
			preview = firstWord(c.Last.From) + ": " + preview
		}
		preview = strings.Join(strings.Fields(preview), " ")
	}
	if c.Unread > 0 {
		badge = m.st.accent.Bold(true).Render(fmt.Sprint(c.Unread))
	}

	// The selection bar's column on the left is mirrored by a margin on the
	// right, so the row sits centered in the sidebar.
	avatar := m.avatar(c)
	text := w - 1 - avatarCols - 1 - 1
	return []string{
		bar + avatar[0] + " " + spread(title, stamp, text) + " ",
		bar + avatar[1] + " " + spread(m.st.muted.Render(preview), badge, text) + " ",
	}
}

// avatar returns the two lines of a chat's picture: the real image when the
// terminal can draw it and it is loaded, a colored initials badge otherwise.
func (m *model) avatar(c *core.Chat) []string {
	if id := m.avatars[c.ID]; id != 0 {
		return termimg.Placeholder(id, avatarCols, avatarRows)
	}
	color := m.st.peer(c.ID)
	fill := lipgloss.NewStyle().Background(color).Foreground(m.st.theme.Background).Bold(true)
	edge := lipgloss.NewStyle().Foreground(color)
	return []string{
		fill.Render(fit(" "+initials(c.Title), avatarCols)),
		edge.Render(strings.Repeat("▀", avatarCols)),
	}
}

// conversation renders the title, the messages and the composer.
func (m *model) conversation(w, h int) []string {
	rows := make([]string, 0, h)
	c := m.chat(m.openID)
	hist := m.histories[m.openID]
	if c == nil || hist == nil {
		for len(rows) < h {
			rows = append(rows, strings.Repeat(" ", w))
		}
		if h > 2 {
			rows[h/2] = center(m.st.muted.Render("select a chat and press enter"), w)
		}
		return rows
	}

	title := " " + m.st.bold.Render(c.Title)
	if c.Username != "" {
		title += m.st.muted.Render("  @" + c.Username)
	}
	rows = append(rows, spread(title, m.st.muted.Render(string(c.Kind))+" ", w))

	rows = append(rows, strings.Repeat(" ", w))

	lines, _ := m.messageLines(hist, w)
	if !hist.loaded {
		lines = []string{m.st.muted.Render(" loading")}
	}
	start, end, pad, overflow := m.shown(len(lines))
	for range pad {
		rows = append(rows, strings.Repeat(" ", w))
	}
	for _, l := range lines[start:end] {
		rows = append(rows, fit(l, w))
	}

	rows = append(rows, strings.Repeat(" ", w))
	if m.draftRows() > 0 {
		rows = append(rows, m.draftLine(w))
	}
	m.input.SetWidth(w - 4)
	m.input.Placeholder = "message " + c.Title
	rows = append(rows, fit(" "+m.input.View(), w))
	return rows[overflow:]
}

// draftLine names the message the composer replies to or edits.
func (m *model) draftLine(w int) string {
	id, label := m.replyTo, "Reply to "
	if m.editing != 0 {
		id, label = m.editing, "Edit"
	}
	msg, _ := m.message(id)
	switch {
	case m.editing != 0:
	case msg.Out:
		label += "You"
	default:
		label += msg.From
	}
	return fit(" "+m.st.accent.Render("▌"+label)+"  "+m.st.muted.Render(strings.Join(strings.Fields(msg.Preview()), " ")), w)
}

// groupGap is how far apart two messages of one sender can be and still be
// drawn together.
const groupGap = 5 * time.Minute

// messageLines lays the history out for a pane of width w: a date line per
// day, then "HH:MM Name: text" wrapped with a hanging indent. Messages from
// one sender close in time stay together, a gap sets the others apart.
// ids holds the message ID of each line, 0 for
// dates and gaps. The message being replied to or edited is marked.
func (m *model) messageLines(h *history, w int) (lines []string, ids []int) {
	const indent = 7 // " HH:MM "
	add := func(id int, line string) {
		lines, ids = append(lines, line), append(ids, id)
	}
	byID := make(map[int]*core.Message, len(h.msgs))
	var day string
	for i := range h.msgs {
		msg := &h.msgs[i]
		byID[msg.ID] = msg

		if d := msg.Date.Format("Mon 2 Jan 2006"); d != day {
			if day != "" {
				add(0, "")
			}
			day = d
			add(0, center(m.st.muted.Render(d), w))
		} else if prev := h.msgs[i-1]; prev.Out != msg.Out || prev.FromID != msg.FromID ||
			prev.Service != msg.Service || msg.Date.Sub(prev.Date) > groupGap {
			add(0, "")
		}

		mark := " "
		if msg.ID == m.replyTo || msg.ID == m.editing {
			mark = m.st.accent.Render("▌")
		}
		if ref := byID[msg.ReplyTo]; ref != nil {
			quote := ansi.Truncate(firstWord(ref.From)+": "+strings.Join(strings.Fields(ref.Preview()), " "), w-indent-3, "…")
			add(msg.ID, mark+strings.Repeat(" ", indent-1)+m.st.muted.Render("↪ "+quote))
		}

		name := lipgloss.NewStyle().Foreground(m.st.peer(msg.FromID)).Render(msg.From)
		if msg.Out {
			name = m.st.accent.Render("You")
		}
		body := msg.Text
		if msg.Media != "" {
			body = strings.TrimSpace(m.st.muted.Render("["+msg.Media+"]") + " " + body)
		}
		text := name + ": " + body
		if msg.Service {
			text = m.st.muted.Render(msg.From + " " + msg.Text)
		}
		if msg.Edited {
			text += m.st.muted.Render(" (edited)")
		}

		for j, l := range strings.Split(ansi.Wrap(text, max(10, w-indent-1), ""), "\n") {
			prefix := mark + strings.Repeat(" ", indent-1)
			if j == 0 {
				prefix = mark + m.st.muted.Render(msg.Date.Format("15:04")) + " "
			}
			add(msg.ID, prefix+l)
		}
	}
	return lines, ids
}

func (m *model) statusLine() string {
	// Each hint is a key and what it does.
	hints := [][2]string{{"↑↓", "move"}, {"enter", "open"}, {"/", "filter"}, {"tab", "compose"}, {"q", "quit"}}
	switch {
	case m.menu != nil:
		hints = [][2]string{{"↑↓", "move"}, {"enter", "pick"}, {"esc", "close"}}
	case m.focus == focusInput && m.editing != 0:
		hints = [][2]string{{"esc", "cancel"}}
	case m.focus == focusInput && m.replyTo != 0:
		hints = [][2]string{{"ctrl+↑↓", "pick"}, {"esc", "cancel"}}
	case m.focus == focusInput:
		hints = [][2]string{{"ctrl+↑↓", "reply"}, {"↑", "edit last"}, {"right click", "more"}, {"esc", "chats"}, {"pgup/pgdn", "scroll"}, {"ctrl+c", "quit"}}
	case m.focus == focusFilter:
		hints = [][2]string{{"type", "to filter"}, {"enter", "open"}, {"esc", "clear"}}
	}
	// An error or a note takes the whole line: an error is usually longer
	// than what is left next to the hints, and matters more than them.
	switch {
	case m.status != "":
		return fit(" "+m.st.err.Render(m.status), m.width)
	case m.note != "":
		return fit(" "+m.st.accent.Render(m.note), m.width)
	}
	right := m.st.muted.Render("tg " + m.version)
	if m.account != "" {
		right = m.st.accent.Render(m.account) + "  " + right
	}
	// Hints come most useful first, and the ones that do not fit are left
	// out whole rather than cut.
	var help strings.Builder
	room := m.width - ansi.StringWidth(right) - 2
	for _, h := range hints {
		hint := " " + m.st.key.Render(h[0]) + " " + m.st.dim.Render(h[1]) + " "
		if room -= ansi.StringWidth(hint); room < 0 {
			break
		}
		help.WriteString(hint)
	}
	return spread(help.String(), right+" ", m.width)
}

// menuWidth is the width of a context menu: its longest label, padded.
func menuWidth(items []menuItem) int {
	w := 0
	for _, item := range items {
		w = max(w, ansi.StringWidth(item.label))
	}
	return w + 3
}

// overlay draws box over row, a line w cells wide, from cell x on.
func overlay(row, box string, x, w int) string {
	left := ansi.Truncate(row, x, "")
	left += strings.Repeat(" ", max(0, x-ansi.StringWidth(left)))
	right := ansi.TruncateLeft(row, x+ansi.StringWidth(box), "")
	return fit(left+ansi.ResetStyle+box+right, w)
}

// fit truncates or pads s to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	return s + strings.Repeat(" ", max(0, w-ansi.StringWidth(s)))
}

// spread puts left and right at the two ends of a w cell line, truncating
// left when they do not both fit.
func spread(left, right string, w int) string {
	rw := ansi.StringWidth(right)
	if rw > 0 {
		rw++ // keep a gap
	}
	if rw >= w {
		return fit(left, w)
	}
	out := fit(left, w-rw)
	if rw > 0 {
		out += " " + right
	}
	return out
}

func center(s string, w int) string {
	pad := max(0, w-ansi.StringWidth(s)) / 2
	return fit(strings.Repeat(" ", pad)+s, w)
}

// shortTime formats a timestamp the way chat lists do: time today, weekday
// within the last week, date otherwise.
func shortTime(t time.Time) string {
	now := time.Now()
	switch age := now.Sub(t); {
	case t.Year() == now.Year() && t.YearDay() == now.YearDay():
		return t.Format("15:04")
	case age < 7*24*time.Hour:
		return t.Format("Mon")
	default:
		return t.Format("2 Jan")
	}
}

func initials(title string) string {
	var out []rune
	for _, word := range strings.Fields(title) {
		if r := []rune(word)[0]; unicode.IsLetter(r) || unicode.IsDigit(r) {
			out = append(out, unicode.ToUpper(r))
		}
		if len(out) == 2 {
			break
		}
	}
	if len(out) == 0 {
		return "?"
	}
	return string(out)
}

func firstWord(s string) string {
	if fields := strings.Fields(s); len(fields) > 0 {
		return fields[0]
	}
	return s
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
