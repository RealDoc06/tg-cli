package tui

import (
	"runtime"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The panel esc opens from the chat list, as in btop: the tg logo over a
// short menu. Settings and Help open in its place, and esc goes back to it.
// A setting changes at once and is saved to config.json.

// page is what the panel shows.
type page int

const (
	pageMain page = iota
	pageSettings
	pageHelp
	pageQuit // not a page: picking it quits
)

// panel is the open panel, sel the highlighted line of its page.
type panel struct {
	page page
	sel  int
}

// mainItems are the lines of the main page, each opening its page.
var mainItems = []page{pageSettings, pageHelp, pageQuit}

var pageNames = map[page]string{pageSettings: "Settings", pageHelp: "Help", pageQuit: "Quit"}

// items is how many lines of the page can be picked.
func (p *panel) items() int {
	switch p.page {
	case pageMain:
		return len(mainItems)
	case pageSettings:
		return len(settingsItems)
	}
	return 0
}

// panelKey handles a key while the panel is open.
func (m *model) panelKey(key string) tea.Cmd {
	p := m.panel
	n := p.items()
	switch key {
	case "up", "k", "shift+tab":
		if n > 0 {
			p.sel = (p.sel + n - 1) % n
		}
	case "down", "j", "tab":
		if n > 0 {
			p.sel = (p.sel + 1) % n
		}
	case "left", "h":
		if p.page == pageSettings {
			return m.changeSetting(-1)
		}
	case "right", "l", "enter", "space":
		return m.pick()
	case "esc", "q":
		m.back()
	}
	return nil
}

// pick acts on the highlighted line: it opens a page, changes a setting or
// quits. On the help page it goes back.
func (m *model) pick() tea.Cmd {
	switch p := m.panel; p.page {
	case pageMain:
		if mainItems[p.sel] == pageQuit {
			return tea.Quit
		}
		*p = panel{page: mainItems[p.sel]}
	case pageSettings:
		return m.changeSetting(1)
	case pageHelp:
		m.back()
	}
	return nil
}

// back goes from a page to the main one, on the line that opened it. From
// the main page it closes the panel.
func (m *model) back() {
	if m.panel.page == pageMain {
		m.panel = nil
		return
	}
	m.panel = &panel{sel: slices.Index(mainItems, m.panel.page)}
}

// setting is one line of the settings page.
type setting struct {
	name  string
	value func(Look) string
	// next returns the change that moves the setting from its value in l to
	// the next (1) or previous (-1) one. It sets the value outright, so
	// saving it over config.json leaves the other settings as they are there.
	next func(l Look, delta int) func(*Look)
}

var settingsItems = []setting{
	{"Theme", func(l Look) string { t, _ := FindTheme(l.Theme); return t.Name }, func(l Look, delta int) func(*Look) {
		t, _ := FindTheme(l.Theme)
		i := slices.IndexFunc(Themes, func(o Theme) bool { return o.Name == t.Name })
		name := Themes[(i+delta+len(Themes))%len(Themes)].Name
		return func(l *Look) { l.Theme = name }
	}},
	{"Message spacing", func(l Look) string { return onOff[l.Spaced] }, func(l Look, _ int) func(*Look) {
		on := !l.Spaced
		return func(l *Look) { l.Spaced = on }
	}},
	{"Chat spacing", func(l Look) string { return onOff[!l.PackedChats] }, func(l Look, _ int) func(*Look) {
		packed := !l.PackedChats
		return func(l *Look) { l.PackedChats = packed }
	}},
	{"Shortcuts", func(l Look) string { return map[bool]string{true: "macOS", false: "PC"}[l.mac()] }, func(l Look, _ int) func(*Look) {
		keys := map[bool]string{true: "pc", false: "mac"}[l.mac()]
		return func(l *Look) { l.Keys = keys }
	}},
}

var onOff = map[bool]string{true: "on", false: "off"}

// mac reports whether shortcuts are written the way Mac keyboards label them.
// Over SSH tg runs on another system than the keyboard, so it can be set.
func (l Look) mac() bool { return l.Keys == "mac" || l.Keys == "" && runtime.GOOS == "darwin" }

// changeSetting moves the highlighted setting by delta, shows it and saves it.
func (m *model) changeSetting(delta int) tea.Cmd {
	change := settingsItems[m.panel.sel].next(m.look, delta)
	change(&m.look)
	t, _ := FindTheme(m.look.Theme)
	if t.Name != m.st.theme.Name {
		m.st = newStyles(t)
		m.paint(&m.input)
		m.paint(&m.filter)
		// Initials badges are pictures, drawn in the old colors.
		for key := range m.images {
			if c := m.chat(key.chat); key.msg == 0 && (c == nil || c.PhotoID == 0) {
				m.drop(key)
			}
		}
	}
	if m.save != nil {
		if err := m.save(change); err != nil {
			m.status = "the setting is not saved: " + err.Error()
		}
	}
	// Spacing changes how many lines there are, and how many chats fit.
	return tea.Batch(m.afterMove(), m.afterScroll())
}

// logo is drawn on the main page: blocks in a gradient, their shadow muted.
var logo = []string{
	"████████╗ ██████╗        ██████╗██╗     ██╗",
	"╚══██╔══╝██╔════╝       ██╔════╝██║     ██║",
	"   ██║   ██║  ███╗█████╗██║     ██║     ██║",
	"   ██║   ██║   ██║╚════╝██║     ██║     ██║",
	"   ██║   ╚██████╔╝      ╚██████╗███████╗██║",
	"   ╚═╝    ╚═════╝        ╚═════╝╚══════╝╚═╝",
}

// panelView renders the open page as a box centered over the panes: where
// the box goes, its lines, and the row of the box its first item is on.
func (m *model) panelView() (x, y int, lines []string, first int) {
	w, title := 38, ""
	var body []string
	switch m.panel.page {
	case pageMain:
		w = len([]rune(logo[0])) + 6
		body = m.logoLines(w - 2)
		body = append(body, "")
		for i, p := range mainItems {
			body = append(body, m.item(center(pageNames[p], w-3), "", i == m.panel.sel, w-2))
		}
		first = len(body) - len(mainItems)
	case pageSettings:
		title = pageNames[pageSettings]
		for i, s := range settingsItems {
			value := m.st.muted.Render(s.value(m.look)) + "    "
			if i == m.panel.sel {
				value = m.st.text.Render("‹ "+s.value(m.look)+" ›") + "  "
			}
			body = append(body, m.item(" "+s.name, value, i == m.panel.sel, w-2))
		}
	case pageHelp:
		w, title = 46, pageNames[pageHelp]
		body = m.helpLines()
	}
	w = min(w, m.width)
	lines = m.box(title, body, w)
	return max(0, (m.width-w)/2), max(0, (m.bodyRows()-len(lines))/2), lines, first + 2
}

// logoLines centers the logo in w cells, with the version under its right
// end as btop has it.
func (m *model) logoLines(w int) []string {
	t := m.st.theme
	// Peers[5] is blue in every theme.
	shades := lipgloss.Blend1D(len(logo), t.Accent, t.Peers[5])
	lw := len([]rune(logo[0]))
	var lines []string
	for i, row := range logo {
		var line strings.Builder
		block := lipgloss.NewStyle().Foreground(shades[i])
		for _, r := range row {
			if r == '█' {
				line.WriteString(block.Render(string(r)))
			} else {
				line.WriteString(m.st.muted.Render(string(r)))
			}
		}
		lines = append(lines, center(line.String(), w))
	}
	pad := (w-lw)/2 + lw
	return append(lines, fit(spread("", m.st.muted.Render(m.version), pad), w))
}

// helpLines lists the shortcuts, written as the keyboard labels them.
func (m *model) helpLines() []string {
	reply := "ctrl+↑↓"
	if m.look.mac() {
		reply = "alt+↑↓"
	}
	keys := [][2]string{
		{"↑↓ jk", "move in the chat list"},
		{"enter", "open a chat, send a message"},
		{"/", "filter chats"},
		{"tab", "chats or composer"},
		{"pgup pgdn", "scroll the conversation"},
		{reply, "pick a message to reply to"},
		{"↑", "edit your last message"},
		{"ctrl+v", "attach the copied image"},
		{"right click", "reply, edit, copy a message"},
		{"ctrl+c", "clear the composer, quit"},
		{"esc", "back, and this menu"},
		{"q", "quit, from the chat list"},
	}
	var lines []string
	for _, k := range keys {
		key := m.keyName(k[0])
		if k[0] == "ctrl+v" && m.look.mac() {
			key = m.keyName("cmd+v")
		}
		lines = append(lines, "  "+m.st.key.Render(key)+strings.Repeat(" ", max(1, 13-len([]rune(key))))+m.st.text.Render(k[1]))
	}
	return lines
}

// item renders a line that can be picked, w cells wide: name on the left,
// value on the right, marked with a bar like the selected chat.
func (m *model) item(name, value string, selected bool, w int) string {
	bar, style := " ", m.st.text
	if selected {
		bar, style = m.st.accent.Render("▌"), m.st.accent.Bold(true)
	}
	return bar + spread(style.Render(name), value, max(0, w-1))
}

// box draws body in a rounded border w cells wide, with title in its top
// edge and a blank row above and below the body.
func (m *model) box(title string, body []string, w int) []string {
	inner := max(0, w-2)
	side := m.st.border.Render("│")
	top := m.st.border.Render("╭" + strings.Repeat("─", inner) + "╮")
	if title != "" {
		title = " " + title + " "
		top = m.st.border.Render("╭─") + m.st.accent.Bold(true).Render(title) +
			m.st.border.Render(strings.Repeat("─", max(0, inner-1-len(title)))+"╮")
	}
	blank := side + strings.Repeat(" ", inner) + side
	lines := []string{ansi.Truncate(top, w, ""), blank}
	for _, line := range body {
		lines = append(lines, side+fit(line, inner)+side)
	}
	return append(lines, blank, m.st.border.Render("╰"+strings.Repeat("─", inner)+"╯"))
}

// keyName writes a key the way the keyboard labels it. On a Mac option and
// command are symbols, followed by an upper case letter: "cmd+v" is ⌘V,
// "alt+↑↓" is ⌥↑↓. ctrl stays a word, as on the key.
func (m *model) keyName(key string) string {
	if !m.look.mac() {
		return key
	}
	out := strings.NewReplacer("alt+", "⌥", "cmd+", "⌘").Replace(key)
	if last := len(out) - 1; out != key && out[last] >= 'a' && out[last] <= 'z' && out[last-1] >= 0x80 {
		out = out[:last] + strings.ToUpper(out[last:])
	}
	return out
}
