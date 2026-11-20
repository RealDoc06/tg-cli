package tui

import (
	"runtime"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
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
	{"Spacing", func(l Look) string { return map[bool]string{true: "on", false: "off"}[l.Spaced] }, func(l Look, _ int) func(*Look) {
		on := !l.Spaced
		return func(l *Look) { l.Spaced = on }
	}},
	{"Shortcuts", func(l Look) string { return map[bool]string{true: "macOS", false: "PC"}[l.mac()] }, func(l Look, _ int) func(*Look) {
		keys := map[bool]string{true: "pc", false: "mac"}[l.mac()]
		return func(l *Look) { l.Keys = keys }
	}},
}

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
	return m.afterScroll() // spacing changes how many lines there are
}

// settingsWidth is the width of the settings menu.
const settingsWidth = 30

// settingsBox is where the settings menu is drawn: centered, a title line
// above one line per setting.
func (m *model) settingsBox() (x, y, w int) {
	w = min(settingsWidth, m.width)
	return max(0, (m.width-w)/2), max(0, (m.bodyRows()-1-len(settingsItems))/2), w
}

// settingsLines renders the settings menu, w cells wide. The highlighted
// setting shows arrows around its value, which change it.
func (m *model) settingsLines(w int) []string {
	lines := []string{m.st.menu.Bold(true).Render(fit(" Settings", w))}
	for i, s := range settingsItems {
		style, value := m.st.menu, "  "+s.value(m.look)+"  "
		if i == m.settings.sel {
			style, value = m.st.menuSel, "‹ "+s.value(m.look)+" ›"
		}
		lines = append(lines, style.Render(spread(" "+s.name, value+" ", w)))
	}
	return lines
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
