package tui

import (
	"runtime"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// The settings menu opens with esc from the chat list, as in btop. A change
// shows at once and is saved to config.json.

// settingsMenu is the open settings menu, sel the highlighted setting.
type settingsMenu struct{ sel int }

// setting is one line of the settings menu.
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
	change := settingsItems[m.settings.sel].next(m.look, delta)
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

// settingsWidth is the width of the settings box, borders included.
const settingsWidth = 38

// The settings box is a rounded border with the title in its top edge and a
// blank row above and below the settings: setting i is on row settingsTop+i.
const settingsTop = 2

// settingsBox is where the settings box is drawn, centered over the panes.
func (m *model) settingsBox() (x, y, w int) {
	w = min(settingsWidth, m.width)
	h := len(settingsItems) + 2*settingsTop
	return max(0, (m.width-w)/2), max(0, (m.bodyRows()-h)/2), w
}

// settingsLines renders the settings box, w cells wide. The highlighted
// setting is marked with a bar, as the selected chat is, and shows arrows
// around its value, which change it.
func (m *model) settingsLines(w int) []string {
	inner := max(0, w-2)
	side := m.st.border.Render("│")
	title := " Settings "
	top := m.st.border.Render("╭─") + m.st.accent.Bold(true).Render(title) +
		m.st.border.Render(strings.Repeat("─", max(0, inner-1-len(title)))+"╮")
	blank := side + strings.Repeat(" ", inner) + side

	lines := []string{ansi.Truncate(top, w, ""), blank}
	for i, s := range settingsItems {
		bar, name, value := " ", m.st.text.Render(s.name), m.st.muted.Render(s.value(m.look))+"    "
		if i == m.settings.sel {
			bar = m.st.accent.Render("▌")
			name = m.st.accent.Bold(true).Render(s.name)
			value = m.st.text.Render("‹ "+s.value(m.look)+" ›") + "  "
		}
		lines = append(lines, side+bar+spread(" "+name, value, max(0, inner-1))+side)
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
