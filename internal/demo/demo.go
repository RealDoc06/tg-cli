// Package demo is an in-memory Backend with made up chats. It backs TG_DEMO=1,
// which lets the TUI and every command run without a Telegram account: for
// trying the tool, for tests and for screenshots that must not leak real chats.
package demo

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/RealDoc06/tg-cli/internal/core"
)

// Backend implements core.Backend over fixture data.
type Backend struct {
	mu       sync.Mutex
	chats    []core.Chat
	messages map[int64][]core.Message
	nextID   int
	events   chan core.Event
}

// channelBase is where supergroup IDs start, as on Telegram.
const channelBase = 1_000_000_000_300

var self = core.User{ID: 1000, Name: "Sam Taylor", Username: "samt", Phone: "15550100"}

// now is fixed so demo output and screenshots are reproducible.
var now = time.Date(2026, 11, 19, 18, 30, 0, 0, time.Local)

type fixture struct {
	chat  core.Chat
	lines []string // "<minutes ago>|<sender, empty for self>|<text>"
}

var fixtures = []fixture{
	{core.Chat{ID: 101, Kind: core.KindUser, Title: "Alice Rossi", Username: "alice", Unread: 2, Pinned: true, PhotoID: 1}, []string{
		"190|Alice Rossi|hey, are we still on for tomorrow?",
		"184||yes, 10am at the usual place",
		"183|Alice Rossi|perfect",
		"12|Alice Rossi|can you bring the charger I left at yours?",
		"11|Alice Rossi|the white one",
	}},
	{core.Chat{ID: -1001000000201, Kind: core.KindGroup, Title: "Homelab", Unread: 5, PhotoID: 2}, []string{
		"1700|Giulia|anyone else seeing the NAS drop off the network at night?",
		"1690|Marco|yes, twice this week. I think the power supply is on its way out",
		"1685||I have a spare 450W one, I can bring it over tomorrow",
		"1680|Giulia|that would be great, thanks",
		"320|Marco|swapped it this morning",
		"318|Marco|[photo]",
		"300|Marco|the storage node is back up, the PSU was the culprit",
		"290|Giulia|nice, did the pool resilver cleanly?",
		"288|Marco|yes, zero errors. It took about four hours for the 6 TB mirror, temperatures stayed under 41 degrees the whole time",
		"95||I'll move the media VM back tonight",
		"40|Giulia|ping me if you need a hand with the migration",
		"22|Marco|backups finished, 412 GB in 38 minutes",
	}},
	{core.Chat{ID: 102, Kind: core.KindUser, Title: "Bob Chen", Username: "bobchen", PhotoID: 3}, []string{
		"1500|Bob Chen|pushed the fix for the flaky test",
		"1490||thanks, reviewing now",
		"1440||merged",
		"75|Bob Chen|release went out without issues 🎉",
	}},
	{core.Chat{ID: -1001000000202, Kind: core.KindChannel, Title: "Go Weekly", Username: "goweekly", Unread: 1, PhotoID: 4}, []string{
		"10080|Go Weekly|Issue 571: range over func in practice",
		"130|Go Weekly|Issue 572: what is new in the garbage collector",
	}},
	{core.Chat{ID: 103, Kind: core.KindUser, Title: "Chiara Bianchi"}, []string{
		"2900|Chiara Bianchi|sent you the invoice",
		"2890||got it, paying today",
		"205|Chiara Bianchi|received, thank you!",
	}},
	{core.Chat{ID: -202, Kind: core.KindGroup, Title: "Family", PhotoID: 5}, []string{
		"4000|Mamma|dinner on Sunday?",
		"3990||I'm in",
		"340|Papà|[photo]",
	}},
	{core.Chat{ID: 104, Kind: core.KindBot, Title: "BotFather", Username: "BotFather", PhotoID: 6}, []string{
		"8000||/newbot",
		"7999|BotFather|Alright, a new bot. How are we going to call it?",
	}},
	{core.Chat{ID: 1000, Kind: core.KindUser, Title: "Saved Messages"}, []string{
		"9000||wifi guest password: correct horse battery staple",
		"610||https://core.telegram.org/api",
	}},
	{core.Chat{ID: 105, Kind: core.KindUser, Title: "Dmitri Volkov", Username: "dvolkov", PhotoID: 7}, []string{
		"11000|Dmitri Volkov|the slides from the talk are up",
		"10990||great talk, thanks for sharing",
	}},
	{core.Chat{ID: -1001000000203, Kind: core.KindChannel, Title: "Terminal Trove", Username: "terminaltrove", PhotoID: 8}, []string{
		"12000|Terminal Trove|Tool of the week: a GPU accelerated terminal multiplexer",
	}},
}

// New returns a demo backend populated with the fixtures.
func New() *Backend {
	b := &Backend{messages: map[int64][]core.Message{}, nextID: 1, events: make(chan core.Event, 16)}
	for _, f := range fixtures {
		for _, line := range f.lines {
			parts := strings.SplitN(line, "|", 3)
			var ago int
			fmt.Sscan(parts[0], &ago)
			m := core.Message{
				ID: b.nextID, ChatID: f.chat.ID, From: parts[1], Text: parts[2],
				Date: now.Add(-time.Duration(ago) * time.Minute),
			}
			if m.From == "" {
				m.From, m.FromID, m.Out = self.Name, self.ID, true
			} else {
				// Any stable ID will do, it only picks the name color.
				for _, r := range m.From {
					m.FromID = m.FromID*31 + int64(r)
				}
			}
			if media, ok := strings.CutPrefix(m.Text, "["); ok {
				m.Media, m.Text = strings.TrimSuffix(media, "]"), ""
			}
			b.nextID++
			b.messages[f.chat.ID] = append(b.messages[f.chat.ID], m)
		}
		chat := f.chat
		msgs := b.messages[chat.ID]
		chat.Last = &msgs[len(msgs)-1]
		b.chats = append(b.chats, chat)
	}
	// Pinned chats first, then most recent first, like the real chat list.
	slices.SortStableFunc(b.chats, func(x, y core.Chat) int {
		if x.Pinned != y.Pinned {
			return cmp.Compare(btoi(y.Pinned), btoi(x.Pinned))
		}
		return y.Last.Date.Compare(x.Last.Date)
	})
	return b
}

func (b *Backend) Self() core.User { return self }

func (b *Backend) Chats(_ context.Context, q core.ChatQuery) ([]core.Chat, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	start := 0
	if q.After != nil {
		for i, c := range b.chats {
			if c.ID == q.After.ID {
				start = i + 1
			}
		}
	}
	end := len(b.chats)
	if q.Limit > 0 {
		end = min(end, start+q.Limit)
	}
	return append([]core.Chat(nil), b.chats[start:end]...), nil
}

func (b *Backend) Resolve(_ context.Context, ref string) (core.Chat, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	needle := strings.ToLower(strings.TrimPrefix(ref, "@"))
	for _, c := range b.chats {
		if fmt.Sprint(c.ID) == ref || strings.ToLower(c.Username) == needle || strings.ToLower(c.Title) == needle ||
			(needle == "me" && c.ID == self.ID) {
			return c, nil
		}
	}
	return core.Chat{}, fmt.Errorf("no chat matches %q, find it with `tg chats -q <text>`", ref)
}

func (b *Backend) History(_ context.Context, chatID int64, q core.HistoryQuery) ([]core.Message, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []core.Message
	for _, m := range b.messages[chatID] {
		if (q.BeforeID == 0 || m.ID < q.BeforeID) && m.ID > q.AfterID {
			out = append(out, m)
		}
	}
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[len(out)-q.Limit:]
	}
	return out, nil
}

// Send stores the message and schedules a canned reply so live updates can be
// seen in the TUI and in `tg watch`.
func (b *Backend) Send(_ context.Context, chatID int64, req core.SendRequest) (core.Message, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	chat, ok := b.chat(chatID)
	if !ok {
		return core.Message{}, fmt.Errorf("unknown chat %d", chatID)
	}
	m := core.Message{ID: b.nextID, ChatID: chatID, From: self.Name, FromID: self.ID, Out: true, Date: time.Now(), Text: req.Text, ReplyTo: req.ReplyTo}
	if req.File != "" {
		m.Media = "file: " + filepath.Base(req.File)
	}
	b.nextID++
	b.messages[chatID] = append(b.messages[chatID], m)

	if chat.Kind == core.KindUser && chatID != self.ID {
		reply := core.Message{ID: b.nextID, ChatID: chatID, From: chat.Title, FromID: chatID, Date: time.Now().Add(time.Second), Text: "👍 (demo reply)"}
		b.nextID++
		time.AfterFunc(time.Second, func() {
			b.mu.Lock()
			b.messages[chatID] = append(b.messages[chatID], reply)
			b.mu.Unlock()
			select {
			case b.events <- core.Event{Kind: core.EventMessage, Chat: *chat, Message: reply}:
			default:
			}
		})
	}
	return m, nil
}

func (b *Backend) MarkRead(_ context.Context, chatID int64, _ int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if chat, ok := b.chat(chatID); ok {
		chat.Unread = 0
	}
	return nil
}

func (b *Backend) CreateGroup(_ context.Context, title string) (core.Chat, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	chat := core.Chat{ID: -channelBase - int64(len(b.chats)), Kind: core.KindGroup, Title: title}
	b.chats = append([]core.Chat{chat}, b.chats...)
	return chat, nil
}

func (b *Backend) Search(_ context.Context, q core.SearchQuery) ([]core.Message, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []core.Message
	for _, c := range b.chats {
		if q.ChatID != 0 && c.ID != q.ChatID {
			continue
		}
		for _, m := range b.messages[c.ID] {
			if strings.Contains(strings.ToLower(m.Text), strings.ToLower(q.Text)) {
				out = append(out, m)
			}
		}
	}
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[len(out)-q.Limit:]
	}
	return out, nil
}

// Avatar paints a deterministic gradient so image rendering can be exercised.
func (b *Backend) Avatar(_ context.Context, chat core.Chat) ([]byte, error) {
	if chat.PhotoID == 0 {
		return nil, fmt.Errorf("chat %d has no photo", chat.ID)
	}
	const size = 160
	hue := float64(chat.PhotoID) * 47
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := range size {
		for x := range size {
			t := float64(x+y) / (2 * size)
			ring := math.Hypot(float64(x)-size*0.5, float64(y)-size*0.42) < size*0.2 ||
				math.Hypot(float64(x)-size*0.5, float64(y)-size*1.02) < size*0.42
			c := hsl(hue+t*60, 0.65, 0.35+t*0.2)
			if ring {
				c = hsl(hue+40, 0.5, 0.82)
			}
			img.SetNRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	err := png.Encode(&buf, img)
	return buf.Bytes(), err
}

func (b *Backend) Download(_ context.Context, chatID int64, msgID int, dir string) (string, error) {
	path := filepath.Join(dir, fmt.Sprintf("demo_%d_%d.txt", chatID, msgID))
	return path, os.WriteFile(path, []byte("demo media\n"), 0o644)
}

func (b *Backend) Watch(ctx context.Context, fn func(core.Event)) error {
	fn(core.Event{Kind: core.EventReady})
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev := <-b.events:
			fn(ev)
		}
	}
}

// chat returns a pointer into b.chats. Callers hold b.mu.
func (b *Backend) chat(id int64) (*core.Chat, bool) {
	for i := range b.chats {
		if b.chats[i].ID == id {
			return &b.chats[i], true
		}
	}
	return nil, false
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func hsl(h, s, l float64) color.NRGBA {
	h = math.Mod(h, 360) / 360
	channel := func(n float64) uint8 {
		k := math.Mod(n+h*12, 12)
		a := s * math.Min(l, 1-l)
		return uint8(255 * (l - a*math.Max(-1, math.Min(math.Min(k-3, 9-k), 1))))
	}
	return color.NRGBA{channel(0), channel(8), channel(4), 255}
}
