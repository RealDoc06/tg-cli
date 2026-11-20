package tui

import (
	"fmt"
	"path/filepath"
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
// from the next, none when the list is packed: chat k starts on row
// 1+chatStride*k, below the list's title.
func (m *model) chatGap() int {
	if m.look.PackedChats {
		return 0
	}
	return 1
}

func (m *model) chatStride() int { return 2 + m.chatGap() }

// bodyRows is how many rows the panes take: all but the status line and the
// rule above it.
func (m *model) bodyRows() int { return max(0, m.height-2) }

// chatsDrawn is how many chats fit in the list below its title, possibly
// none. listRows is the same at least one, for paging and keeping the
// selection in view.
func (m *model) chatsDrawn() int { return (m.bodyRows() - 1 + m.chatGap()) / m.chatStride() }

func (m *model) listRows() int { return max(1, m.chatsDrawn()) }

// conversationWidth is the width of the conversation pane, also while a
// narrow terminal shows the chat list instead.
func (m *model) conversationWidth() int {
	if m.width < narrowWidth {
		return m.width
	}
	_, rw := m.layout()
	return rw
}

// The conversation pane is, top to bottom: the title and a gap, the messages,
// a rule, the reply or edit being written when there is one, and the composer.
const messagesTop = 2

func (m *model) draftRows() int {
	if m.replyTo != 0 || m.editing != 0 {
		return 1
	}
	return 0
}

// messageRows is how many lines of conversation fit.
func (m *model) messageRows() int { return max(1, m.bodyRows()-messagesTop-m.draftRows()-2) }

// shown picks the n lines of conversation that are on screen: lines[start:end],
// drawn below pad empty rows. overflow is how many rows at the top of the pane
// a tiny terminal cuts off.
func (m *model) shown(n int) (start, end, pad, overflow int) {
	view := m.messageRows()
	// Clamped here as well: a message that got shorter while the pane was
	// scrolled must not leave it blank.
	end = max(0, n-min(m.scroll, max(0, n-view)))
	start = max(0, end-view)
	overflow = max(0, messagesTop+view+m.draftRows()+2-m.bodyRows())
	return start, end, view - (end - start), overflow
}

// messageAt is the message drawn on screen row y.
func (m *model) messageAt(y int) (core.Message, bool) {
	_, msg, ok := m.lineAt(y)
	return msg, ok
}

// lineAt is the line of conversation drawn on screen row y, and its message.
func (m *model) lineAt(y int) (msgLine, core.Message, bool) {
	h := m.histories[m.openID]
	_, rw := m.layout()
	if h == nil || !h.loaded || rw == 0 {
		return msgLine{}, core.Message{}, false
	}
	lines := m.messageLines(h, rw)
	start, end, pad, overflow := m.shown(len(lines))
	i := start + y - (messagesTop - overflow) - pad
	if i < start || i >= end || lines[i].msg < 0 {
		return msgLine{}, core.Message{}, false
	}
	return lines[i], h.msgs[lines[i].msg], true
}

// revealMessage scrolls the conversation so message id is on screen.
func (m *model) revealMessage(id int) {
	h := m.histories[m.openID]
	_, rw := m.layout()
	if h == nil || rw == 0 {
		return
	}
	lines := m.messageLines(h, rw)
	first, last := -1, -1
	for i, l := range lines {
		if l.msg >= 0 && h.msgs[l.msg].ID == id {
			last = i
			if first < 0 {
				first = i
			}
		}
	}
	if first < 0 {
		return
	}
	if end := len(lines) - m.scroll; last >= end {
		m.scroll = len(lines) - 1 - last
	}
	if start := len(lines) - m.scroll - m.messageRows(); first < start {
		m.scroll = len(lines) - first - m.messageRows()
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
	body := m.bodyRows()
	var left, right []string
	rule := -1 // the row of the rule above the composer
	if lw > 0 {
		left = m.chatList(lw, body)
	}
	if rw > 0 {
		right, rule = m.conversation(rw, body)
	}

	rows := make([]string, 0, m.height)
	for i := range body {
		switch {
		case lw > 0 && rw > 0 && i == rule:
			rows = append(rows, left[i]+m.st.border.Render("├")+right[i])
		case lw > 0 && rw > 0:
			rows = append(rows, left[i]+m.st.border.Render("│")+right[i])
		case lw > 0:
			rows = append(rows, left[i])
		default:
			rows = append(rows, right[i])
		}
	}
	// A rule sets the hints apart from the panes.
	if m.height >= 2 {
		line := strings.Repeat("─", m.width)
		if lw > 0 && rw > 0 {
			line = strings.Repeat("─", lw) + "┴" + strings.Repeat("─", rw)
		}
		rows = append(rows, m.st.border.Render(line))
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
	if m.settings != nil {
		x, y, w := m.settingsBox()
		for i, line := range m.settingsLines(w) {
			if y+i < len(rows) {
				rows[y+i] = overlay(rows[y+i], line, x, m.width)
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

// chatList renders the filter line followed by two lines per chat, set apart
// by chatGap blank ones.
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
		gap := min(i-m.top, 1) * m.chatGap() // none above the first
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
		preview = m.st.muted.Render(strings.Join(strings.Fields(preview), " "))
	}
	// A draft takes the place of the last message, except while it is
	// being written.
	if d := m.draft(c.ID); d != "" && (c.ID != m.openID || m.focus != focusInput) {
		preview = m.st.err.Render("[draft]") + " " + m.st.muted.Render(strings.Join(strings.Fields(d), " "))
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
		bar + avatar[1] + " " + spread(preview, badge, text) + " ",
	}
}

// avatar returns the two lines of a chat's picture: the photo or initials
// badge uploaded to the terminal when it can draw them, colored initials in
// cells otherwise.
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

// conversation renders the title, the messages and the composer, and tells
// which row is the rule above the composer, -1 for none.
func (m *model) conversation(w, h int) ([]string, int) {
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
		return rows, -1
	}

	title := " " + m.st.bold.Render(c.Title)
	if c.Username != "" {
		title += m.st.muted.Render("  @" + c.Username)
	}
	rows = append(rows, spread(title, m.st.muted.Render(string(c.Kind))+" ", w))
	rows = append(rows, strings.Repeat(" ", w))

	lines := m.messageLines(hist, w)
	if !hist.loaded {
		lines = []msgLine{{text: m.st.muted.Render(" loading"), msg: -1}}
	}
	start, end, pad, overflow := m.shown(len(lines))
	for range pad {
		rows = append(rows, strings.Repeat(" ", w))
	}
	for _, l := range lines[start:end] {
		rows = append(rows, fit(l.text, w))
	}

	rule := len(rows)
	rows = append(rows, m.st.border.Render(strings.Repeat("─", w)))
	if m.draftRows() > 0 {
		rows = append(rows, m.draftLine(w))
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
	m.input.SetCursor(m.input.Position()) // SetWidth alone keeps the old visible window
	rows = append(rows, fit(" "+m.input.View(), w))
	return rows[overflow:], rule - overflow
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
// drawn together when spaced.
const groupGap = 5 * time.Minute

// photoIndent is the hanging indent of message bodies: " HH:MM ".
const photoIndent = 7

// msgLine is one line of the conversation, msg is the index of the message
// it belongs to, -1 for date lines. keys are the bot buttons drawn on it.
type msgLine struct {
	text string
	msg  int
	keys []key
}

// key is a button of a bot's keyboard, drawn w cells wide from cell x of the
// conversation pane.
type key struct {
	x, w   int
	button core.Button
}

// messageLines lays the history out for a pane of width w: a date line per
// day, then "HH:MM Name: text" wrapped with a hanging indent, and the picture
// below when it is loaded. Spaced, a gap sets apart a new sender or a pause,
// and each day; otherwise messages are packed. Gaps and dates belong to no
// message. The message being replied to or edited is marked.
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
			if day != "" && m.look.Spaced {
				lines = append(lines, msgLine{text: "", msg: -1})
			}
			day = d
			lines = append(lines, msgLine{text: m.dayLine(d, w), msg: -1})
		} else if prev := h.msgs[i-1]; m.look.Spaced && (prev.Out != msg.Out || prev.FromID != msg.FromID ||
			prev.Service != msg.Service || msg.Date.Sub(prev.Date) > groupGap) {
			lines = append(lines, msgLine{text: "", msg: -1})
		}

		// The message being replied to or edited is marked in the first
		// column, which is otherwise blank.
		mark, padded := " ", pad
		if msg.ID == m.replyTo || msg.ID == m.editing {
			mark = m.st.accent.Render("▌")
			padded = mark + pad[1:]
		}
		if ref := byID[msg.ReplyTo]; ref != nil {
			quote := ansi.Truncate(firstWord(ref.From)+": "+strings.Join(strings.Fields(ref.Preview()), " "), w-indent-3, "…")
			lines = append(lines, msgLine{text: padded + m.st.muted.Render("↪ "+quote), msg: i})
		}

		name := lipgloss.NewStyle().Foreground(m.st.peer(msg.FromID)).Render(msg.From)
		if msg.Out {
			name = m.st.accent.Render("You")
		}
		// A picture that is drawn replaces its "[photo]" label, unless the
		// pane became too narrow for it. A video keeps it for its duration.
		// Until it is drawn, its room is kept when its size is known: the
		// room its load will land in, once that started.
		pic := m.images[imageKey{msg.ChatID, msg.ID}]
		if msg.Picture == core.NoPicture || pic.cols > w-indent-1 {
			pic = shown{}
		}
		keptRows := pic.rows
		if pic.id == 0 && keptRows == 0 {
			_, keptRows = m.pictureCells(*msg, w)
		}
		body := msg.Text
		if msg.Media != "" && (keptRows == 0 || msg.Picture == core.PictureVideo) {
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
			prefix := padded
			if j == 0 {
				prefix = mark + m.st.muted.Render(msg.Date.Format("15:04")) + " "
			}
			lines = append(lines, msgLine{text: prefix + l, msg: i})
		}
		if pic.id != 0 {
			for _, l := range termimg.Placeholder(pic.id, pic.cols, pic.rows) {
				lines = append(lines, msgLine{text: padded + l, msg: i})
			}
		} else {
			for range keptRows {
				lines = append(lines, msgLine{text: padded, msg: i})
			}
		}
		for _, l := range m.buttonLines(msg.Buttons, w-indent-1) {
			for k := range l.keys {
				l.keys[k].x += indent
			}
			l.text, l.msg = padded+l.text, i
			lines = append(lines, l)
		}
	}
	return lines
}

// buttonCells shares free cells among buttons whose labels are widths wide.
// They are equal when the widest label fits, as Telegram draws them. Else
// each label gets its own width and the rest is shared, so a crowded row stays
// readable, and only labels that cannot all fit are cut short. Spare cells go
// one each to the first buttons.
func buttonCells(widths []int, free int) []int {
	k, total := len(widths), 0
	for _, w := range widths {
		total += w
	}
	base, spare := make([]int, k), free
	if slices.Max(widths) > free/k && total <= free {
		copy(base, widths)
		spare = free - total
	}
	cells := make([]int, k)
	for j := range cells {
		cells[j] = base[j] + spare/k
		if j < spare%k {
			cells[j]++
		}
	}
	return cells
}

// dayLine is the date that starts a day of messages, centered on a rule.
func (m *model) dayLine(date string, w int) string {
	label := " " + date + " "
	side := max(0, w-2-ansi.StringWidth(label))
	return fit(" "+m.st.border.Render(strings.Repeat("─", side/2))+m.st.muted.Render(label)+
		m.st.border.Render(strings.Repeat("─", side-side/2)), w)
}

// buttonLines draws a bot's inline keyboard in at most w cells: every row as
// wide, its buttons sharing it as buttonCells does. A blank line sets rows
// apart, as a blank cell does buttons. Keys are placed from cell 0, and the
// lines belong to no message yet.
func (m *model) buttonLines(keyboard [][]core.Button, w int) []msgLine {
	label := func(b core.Button) string { return strings.Join(strings.Fields(b.Text), " ") }
	width := 0
	for _, row := range keyboard {
		widest := 0
		for _, b := range row {
			widest = max(widest, ansi.StringWidth(label(b)))
		}
		width = max(width, len(row)*(widest+4)-1)
	}
	width = min(width, w)
	var lines []msgLine
	for _, row := range keyboard {
		if len(row) == 0 || width < 2*len(row)-1 {
			continue
		}
		widths := make([]int, len(row))
		for j, b := range row {
			widths[j] = max(1, ansi.StringWidth(label(b))) // a blank one still shows
		}
		var line strings.Builder
		var keys []key
		x := 0
		for j, cell := range buttonCells(widths, width-len(row)+1) { // one cell between buttons
			line.WriteString(m.st.menu.Render(center(ansi.Truncate(label(row[j]), cell, "…"), cell)))
			keys = append(keys, key{x, cell, row[j]})
			x += cell + 1
			if j < len(row)-1 {
				line.WriteString(" ")
			}
		}
		if len(lines) > 0 {
			lines = append(lines, msgLine{msg: -1})
		}
		lines = append(lines, msgLine{text: line.String(), msg: -1, keys: keys})
	}
	return lines
}

func (m *model) statusLine() string {
	// Each hint is a key and what it does.
	hints := [][2]string{{"↑↓", "move"}, {"enter", "open"}, {"/", "filter"}, {"tab", "compose"}, {"esc", "settings"}, {"q", "quit"}}
	if m.filter.Value() != "" {
		hints[4] = [2]string{"esc", "clear filter"}
	}
	// Both work everywhere, but on a Mac ctrl+arrows switch desktops, and
	// the terminal's own paste brings images as ctrl+v does.
	reply, paste := "ctrl+↑↓", "ctrl+v"
	if m.look.mac() {
		reply, paste = "alt+↑↓", "cmd+v"
	}
	quit := "quit"
	if m.composing() {
		quit = "clear"
	}
	switch {
	case m.settings != nil:
		hints = [][2]string{{"↑↓", "move"}, {"←→", "change"}, {"esc", "close"}}
	case m.menu != nil:
		hints = [][2]string{{"↑↓", "move"}, {"enter", "pick"}, {"esc", "close"}}
	case m.focus == focusInput && m.editing != 0:
		hints = [][2]string{{"esc", "cancel"}}
	case m.focus == focusInput && m.replyTo != 0:
		hints = [][2]string{{reply, "pick"}, {"esc", "cancel"}}
	case m.focus == focusInput:
		hints = [][2]string{{reply, "reply"}, {"↑", "edit last"}, {"right click", "more"}, {paste, "paste image"}, {"esc", "chats"}, {"pgup/pgdn", "scroll"}, {"ctrl+c", quit}}
		if m.attached() {
			hints[3] = [2]string{"backspace", "remove attachment"}
		}
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
		hint := " " + m.st.key.Render(m.keyName(h[0])) + " " + m.st.dim.Render(h[1]) + " "
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
