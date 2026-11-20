package tui

import (
	"fmt"
	"path/filepath"
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

// listRows is how many chats fit in the list, messageRows how many lines of
// conversation fit above the composer.
func (m *model) listRows() int    { return max(1, (m.height-2)/2) }
func (m *model) messageRows() int { return max(1, m.height-3) }

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

	avatar := m.avatar(c)
	text := w - 1 - avatarCols - 1
	return []string{
		bar + avatar[0] + " " + spread(title, stamp, text),
		bar + avatar[1] + " " + spread(m.st.muted.Render(preview), badge, text),
	}
}

// avatar returns the two lines of a chat's picture: the real image when the
// terminal can draw it and it is loaded, a colored initials badge otherwise.
func (m *model) avatar(c *core.Chat) []string {
	if img := m.images[imageKey{chat: c.ID}]; img.id != 0 {
		return termimg.Placeholder(img.id, avatarCols, avatarRows)
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

	lines := m.messageLines(hist, w)
	if !hist.loaded {
		lines = []msgLine{{m.st.muted.Render(" loading"), -1}}
	}
	view := h - 2
	end := max(0, len(lines)-m.scroll)
	start := max(0, end-view)
	for range view - (end - start) {
		rows = append(rows, strings.Repeat(" ", w))
	}
	for _, l := range lines[start:end] {
		rows = append(rows, fit(l.text, w))
	}

	// An attachment shows in the prompt, the text becomes its caption.
	m.input.Prompt, m.input.Placeholder = "> ", "message "+c.Title
	switch {
	case m.attach.Photo != nil:
		m.input.Prompt, m.input.Placeholder = "[image] > ", "add a caption"
	case m.attach.File != "":
		name := strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, filepath.Base(m.attach.File))
		m.input.Prompt, m.input.Placeholder = "["+ansi.Truncate(name, max(4, w/3), "…")+"] > ", "add a caption"
	}
	m.input.SetWidth(max(1, w-2-ansi.StringWidth(m.input.Prompt)))
	rows = append(rows, fit(" "+m.input.View(), w))
	return rows
}

// photoIndent is the hanging indent of message bodies: " HH:MM ".
const photoIndent = 7

// msgLine is one line of the conversation, msg is the index of the message
// it belongs to, -1 for date lines.
type msgLine struct {
	text string
	msg  int
}

// messageLines lays the history out for a pane of width w: a date line per
// day, then "HH:MM Name: text" wrapped with a hanging indent, and the photo
// below when it is loaded.
func (m *model) messageLines(h *history, w int) []msgLine {
	const indent = photoIndent
	pad := strings.Repeat(" ", indent)
	byID := make(map[int]*core.Message, len(h.msgs))
	var lines []msgLine
	var day string
	for i := range h.msgs {
		msg := &h.msgs[i]
		byID[msg.ID] = msg

		if d := msg.Date.Format("Mon 2 Jan 2006"); d != day {
			day = d
			lines = append(lines, msgLine{center(m.st.muted.Render(d), w), -1})
		}
		if ref := byID[msg.ReplyTo]; ref != nil {
			quote := ansi.Truncate(firstWord(ref.From)+": "+strings.Join(strings.Fields(ref.Preview()), " "), w-indent-3, "…")
			lines = append(lines, msgLine{pad + m.st.muted.Render("↪ "+quote), i})
		}

		name := lipgloss.NewStyle().Foreground(m.st.peer(msg.FromID)).Render(msg.From)
		if msg.Out {
			name = m.st.accent.Render("You")
		}
		// A photo that is drawn replaces its "[photo]" label, unless the
		// pane became too narrow for it.
		photo := m.images[imageKey{msg.ChatID, msg.ID}]
		if msg.Media != core.MediaPhoto || photo.cols > w-indent-1 {
			photo = shown{}
		}
		body := msg.Text
		if msg.Media != "" && photo.id == 0 {
			body = strings.TrimSpace(m.st.muted.Render("["+msg.Media+"]") + " " + body)
		}
		text := strings.TrimSpace(name + ": " + body)
		if msg.Service {
			text = m.st.muted.Render(msg.From + " " + msg.Text)
		}
		if msg.Edited {
			text += m.st.muted.Render(" (edited)")
		}

		for j, l := range strings.Split(ansi.Wrap(text, max(10, w-indent-1), ""), "\n") {
			prefix := pad
			if j == 0 {
				prefix = " " + m.st.muted.Render(msg.Date.Format("15:04")) + " "
			}
			lines = append(lines, msgLine{prefix + l, i})
		}
		if photo.id != 0 {
			for _, l := range termimg.Placeholder(photo.id, photo.cols, photo.rows) {
				lines = append(lines, msgLine{pad + l, i})
			}
		}
	}
	return lines
}

func (m *model) statusLine() string {
	// Each hint is a key and what it does.
	hints := [][2]string{{"↑↓", "move"}, {"enter", "open"}, {"/", "filter"}, {"tab", "compose"}, {"q", "quit"}}
	switch m.focus {
	case focusInput:
		hints = [][2]string{{"enter", "send"}, {"ctrl+v", "paste image"}, {"pgup/pgdn", "scroll"}, {"esc", "chats"}, {"ctrl+c", "quit"}}
		if m.attached() {
			hints[1] = [2]string{"backspace", "remove attachment"}
		}
	case focusFilter:
		hints = [][2]string{{"type", "to filter"}, {"enter", "open"}, {"esc", "clear"}}
	}
	// An error takes the whole line: it is usually longer than what is left
	// next to the hints, and matters more than them.
	if m.status != "" {
		return fit(" "+m.st.err.Render(m.status), m.width)
	}
	right := m.st.muted.Render("tg " + m.version)
	if m.account != "" {
		right = m.st.accent.Render(m.account) + "  " + right
	}
	var help strings.Builder
	for _, h := range hints {
		help.WriteString(" " + m.st.text.Render(h[0]) + " " + m.st.muted.Render(h[1]) + " ")
	}
	return spread(help.String(), right+" ", m.width)
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
