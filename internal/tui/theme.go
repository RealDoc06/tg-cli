package tui

import (
	"image/color"
	"slices"

	"charm.land/lipgloss/v2"
)

// Theme is a palette for the whole interface. The TUI paints the terminal's
// own background and foreground with it while it runs, so a theme looks the
// same whatever colors the terminal was set up with.
type Theme struct {
	Name       string
	Background color.Color
	Text       color.Color
	Muted      color.Color // timestamps, previews, hints
	Border     color.Color // the line between the panes
	Accent     color.Color // selection, the open chat, unread counts, own name
	Error      color.Color
	// Peers color names and initials, picked by peer ID.
	Peers []color.Color
}

// DefaultTheme is used when config.json names none.
const DefaultTheme = "mocha"

// Themes lists every theme, in the order `tg config` shows them. The four
// Catppuccin flavors use their base, text, subtext0, surface1, sky and red,
// with red, peach, mauve, green, teal, blue and pink for peers.
var Themes = []Theme{
	palette("mocha", "#1e1e2e", "#cdd6f4", "#a6adc8", "#45475a", "#89dceb", "#f38ba8",
		"#f38ba8", "#fab387", "#cba6f7", "#a6e3a1", "#94e2d5", "#89b4fa", "#f5c2e7"),
	palette("macchiato", "#24273a", "#cad3f5", "#a5adcb", "#494d64", "#91d7e3", "#ed8796",
		"#ed8796", "#f5a97f", "#c6a0f6", "#a6da95", "#8bd5ca", "#8aadf4", "#f5bde6"),
	palette("frappe", "#303446", "#c6d0f5", "#a5adce", "#51576d", "#99d1db", "#e78284",
		"#e78284", "#ef9f76", "#ca9ee6", "#a6d189", "#81c8be", "#8caaee", "#f4b8e4"),
	palette("latte", "#eff1f5", "#4c4f69", "#6c6f85", "#bcc0cc", "#04a5e5", "#d20f39",
		"#d20f39", "#fe640b", "#8839ef", "#40a02b", "#179299", "#1e66f5", "#ea76cb"),
	// Telegram's own colors.
	palette("dark", "#000000", "#ffffff", "#8a8a8a", "#303030", "#2aabee", "#ff6b6b",
		"#e17076", "#faa774", "#a695e7", "#7bc862", "#6ec9cb", "#65aadd", "#ee7aae"),
	palette("light", "#ffffff", "#000000", "#707579", "#dadce0", "#3390ec", "#e53935",
		"#cc5049", "#d67722", "#955cdb", "#40a920", "#309eba", "#368ad1", "#c7508b"),
}

func palette(name, background, text, muted, border, accent, errColor string, peers ...string) Theme {
	t := Theme{
		Name: name, Background: lipgloss.Color(background), Text: lipgloss.Color(text),
		Muted: lipgloss.Color(muted), Border: lipgloss.Color(border),
		Accent: lipgloss.Color(accent), Error: lipgloss.Color(errColor),
	}
	for _, p := range peers {
		t.Peers = append(t.Peers, lipgloss.Color(p))
	}
	return t
}

// ThemeNames lists the names `tg config theme` accepts.
func ThemeNames() []string {
	names := make([]string, len(Themes))
	for i, t := range Themes {
		names[i] = t.Name
	}
	return names
}

// FindTheme returns the theme called name, the default one for "". For a
// name that is not a theme it returns the default and false.
func FindTheme(name string) (Theme, bool) {
	if name == "" {
		name = DefaultTheme
	}
	i := slices.IndexFunc(Themes, func(t Theme) bool { return t.Name == name })
	if i < 0 {
		t, _ := FindTheme(DefaultTheme)
		return t, false
	}
	return Themes[i], true
}

// styles are the lipgloss styles the views draw with, made from a theme.
type styles struct {
	theme                             Theme
	text, muted, border, accent, bold lipgloss.Style
	err                               lipgloss.Style
	key, dim                          lipgloss.Style // hints: the key stands out, what it does recedes
	menu, menuSel                     lipgloss.Style // context menu items, and the selected one
}

func newStyles(t Theme) styles {
	fg := func(col color.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(col) }
	return styles{
		theme: t, text: fg(t.Text), muted: fg(t.Muted), border: fg(t.Border),
		accent: fg(t.Accent), bold: lipgloss.NewStyle().Bold(true), err: fg(t.Error),
		key: fg(t.Accent).Bold(true),
		// A third of the way from muted to the background.
		dim:     fg(lipgloss.Blend1D(4, t.Muted, t.Background)[1]),
		menu:    lipgloss.NewStyle().Background(t.Border).Foreground(t.Text),
		menuSel: lipgloss.NewStyle().Background(t.Accent).Foreground(t.Background),
	}
}

// peer is the color of a user or chat, stable across runs.
func (s styles) peer(id int64) color.Color {
	return s.theme.Peers[abs(id)%int64(len(s.theme.Peers))]
}
