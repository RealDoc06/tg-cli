package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/RealDoc06/tg-cli/internal/core"
	"github.com/RealDoc06/tg-cli/internal/termimg"
)

var (
	accent = lipgloss.NewStyle().Foreground(lipgloss.Color("#2AABEE"))
	dim    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	bold   = lipgloss.NewStyle().Bold(true)
	errSty = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF6B6B"))

	// peerColors are the name and initials colors, picked by peer ID.
	peerColors = []string{"#E17076", "#FAA774", "#A695E7", "#7BC862", "#6EC9CB", "#65AADD", "#EE7AAE"}
)

func peerColor(id int64) lipgloss.Style {
	if id < 0 {
		id = -id
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(peerColors[id%int64(len(peerColors))]))
}

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

// listRows is how many chats fit in the list, messageRows how many lines of
// conversation fit above the composer.
func (m *model) listRows() int    { return max(1, (m.height-2)/2) }
func (m *model) messageRows() int { return max(1, m.height-3) }

func (m *model) View() tea.View {
	var v tea.View
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "tg"
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
			rows = append(rows, left[i]+dim.Render("│")+right[i])
		case lw > 0:
			rows = append(rows, left[i])
		default:
			rows = append(rows, right[i])
		}
	}
	rows = append(rows, m.statusLine())
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
		rows = append(rows, fit(" "+bold.Render("Chats"), w))
	}

	chats := m.visible()
	if len(chats) == 0 && h > 1 {
		hint := "loading"
		if m.chatsDone {
			hint = "no chats"
		}
		rows = append(rows, fit(" "+dim.Render(hint), w))
	}
	for i := m.top; i < len(chats) && len(rows)+1 < h; i++ {
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
		bar = dim.Render("▌")
		if m.focus != focusInput {
			bar = accent.Render("▌")
		}
	}

	title := c.Title
	switch {
	case c.ID == m.openID:
		title = accent.Bold(true).Render(title)
	case selected || c.Unread > 0:
		title = bold.Render(title)
	}

	var stamp, preview, badge string
	if c.Last != nil {
		stamp = dim.Render(shortTime(c.Last.Date))
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
		badge = accent.Bold(true).Render(fmt.Sprint(c.Unread))
	}

	avatar := m.avatar(c)
	text := w - 1 - avatarCols - 1
	return []string{
		bar + avatar[0] + " " + spread(title, stamp, text),
		bar + avatar[1] + " " + spread(dim.Render(preview), badge, text),
	}
}

// avatar returns the two lines of a chat's picture: the real image when the
// terminal can draw it and it is loaded, a colored initials badge otherwise.
func (m *model) avatar(c *core.Chat) []string {
	if id := m.avatars[c.ID]; id != 0 {
		return termimg.Placeholder(id, avatarCols, avatarRows)
	}
	color := peerColors[abs(c.ID)%int64(len(peerColors))]
	fill := lipgloss.NewStyle().Background(lipgloss.Color(color)).Foreground(lipgloss.Color("#000000")).Bold(true)
	edge := lipgloss.NewStyle().Foreground(lipgloss.Color(color))
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
			rows[h/2] = center(dim.Render("select a chat and press enter"), w)
		}
		return rows
	}

	title := " " + bold.Render(c.Title)
	if c.Username != "" {
		title += dim.Render("  @" + c.Username)
	}
	rows = append(rows, spread(title, dim.Render(string(c.Kind))+" ", w))

	lines := m.messageLines(hist, w)
	if !hist.loaded {
		lines = []string{dim.Render(" loading")}
	}
	view := h - 2
	end := max(0, len(lines)-m.scroll)
	start := max(0, end-view)
	for range view - (end - start) {
		rows = append(rows, strings.Repeat(" ", w))
	}
	for _, l := range lines[start:end] {
		rows = append(rows, fit(l, w))
	}

	m.input.SetWidth(w - 4)
	m.input.Placeholder = "message " + c.Title
	rows = append(rows, fit(" "+m.input.View(), w))
	return rows
}

// messageLines lays the history out for a pane of width w: a date line per
// day, then "HH:MM Name: text" wrapped with a hanging indent.
func (m *model) messageLines(h *history, w int) []string {
	const indent = 7 // " HH:MM "
	byID := make(map[int]*core.Message, len(h.msgs))
	var lines []string
	var day string
	for i := range h.msgs {
		msg := &h.msgs[i]
		byID[msg.ID] = msg

		if d := msg.Date.Format("Mon 2 Jan 2006"); d != day {
			day = d
			lines = append(lines, center(dim.Render(d), w))
		}
		if ref := byID[msg.ReplyTo]; ref != nil {
			quote := ansi.Truncate(firstWord(ref.From)+": "+strings.Join(strings.Fields(ref.Preview()), " "), w-indent-3, "…")
			lines = append(lines, strings.Repeat(" ", indent)+dim.Render("↪ "+quote))
		}

		name := peerColor(msg.FromID).Render(msg.From)
		if msg.Out {
			name = accent.Render("You")
		}
		body := msg.Text
		if msg.Media != "" {
			body = strings.TrimSpace(dim.Render("["+msg.Media+"]") + " " + body)
		}
		text := name + ": " + body
		if msg.Service {
			text = dim.Render(msg.From + " " + msg.Text)
		}
		if msg.Edited {
			text += dim.Render(" (edited)")
		}

		for j, l := range strings.Split(ansi.Wrap(text, max(10, w-indent-1), ""), "\n") {
			prefix := strings.Repeat(" ", indent)
			if j == 0 {
				prefix = " " + dim.Render(msg.Date.Format("15:04")) + " "
			}
			lines = append(lines, prefix+l)
		}
	}
	return lines
}

func (m *model) statusLine() string {
	hints := "↑↓ move  enter open  / filter  tab compose  q quit"
	switch m.focus {
	case focusInput:
		hints = "enter send  pgup/pgdn scroll  esc chats  ctrl+c quit"
	case focusFilter:
		hints = "type to filter  enter open  esc clear"
	}
	right := dim.Render("tg " + m.version)
	if m.status != "" {
		right = errSty.Render(m.status)
	}
	return spread(" "+dim.Render(hints), right+" ", m.width)
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
