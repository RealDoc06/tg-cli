// Package demo is an in-memory Backend with made up chats. It backs TG_DEMO=1,
// which lets the TUI and every command run without a Telegram account: for
// trying the tool, for tests and for screenshots that must not leak real chats.
package demo

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	_ "embed"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg" // the video's still
	"image/png"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/RealDoc06/tg-cli/internal/core"
	"github.com/RealDoc06/tg-cli/internal/lottie"
	_ "golang.org/x/image/webp" // the sticker's still
)

// Backend implements core.Backend over fixture data.
type Backend struct {
	mu       sync.Mutex
	chats    []core.Chat
	messages map[int64][]core.Message
	// photos holds the images sent as photos, by message ID.
	photos map[int][]byte
	nextID int
	events chan core.Event
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
		"21|Marco|[video 0:02]",
	}},
	{core.Chat{ID: 102, Kind: core.KindUser, Title: "Bob Chen", Username: "bobchen", PhotoID: 3}, []string{
		"1500|Bob Chen|pushed the fix for the flaky test",
		"1490||thanks, reviewing now",
		"1440||merged",
		"75|Bob Chen|release went out without issues 🎉",
		"74|Bob Chen|[sticker 😄]",
	}},
	{core.Chat{ID: -1001000000202, Kind: core.KindChannel, Title: "Go Weekly", Username: "goweekly", Unread: 1, PhotoID: 4}, []string{
		"10080|Go Weekly|Issue 571: range over func in practice",
		"130|Go Weekly|Issue 572: what is new in the garbage collector",
	}},
	{core.Chat{ID: 103, Kind: core.KindUser, Title: "Chiara Bianchi"}, []string{
		"2900|Chiara Bianchi|sent you the invoice",
		"2890||got it, paying today",
		"205|Chiara Bianchi|received, thank you!",
		"204|Chiara Bianchi|[sticker ❤️]",
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
	b := &Backend{messages: map[int64][]core.Message{}, photos: map[int][]byte{}, nextID: 1, events: make(chan core.Event, 16)}
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
				m.Picture = pictureKind(m.Media)
				m.PictureSize = pictureSize(m.Media, m.Picture)
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
	photo := req.Photo
	if photo == nil && core.IsPhotoFile(req.File) {
		var err error
		if photo, err = os.ReadFile(req.File); err != nil {
			return core.Message{}, err
		}
	}
	switch {
	case photo != nil:
		m.Media, m.Picture, m.PictureSize = core.MediaPhoto, core.PicturePhoto, stillSize(photo)
		b.photos[m.ID] = photo
	case req.File != "":
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
			ev := core.Event{Kind: core.EventMessage, Chat: *chat, Message: reply}
			b.mu.Unlock()
			select {
			case b.events <- ev:
			default:
			}
		})
	}
	return m, nil
}

func (b *Backend) Edit(_ context.Context, chatID int64, msgID int, text string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	msgs := b.messages[chatID]
	i := slices.IndexFunc(msgs, func(m core.Message) bool { return m.ID == msgID })
	switch {
	case i < 0:
		return fmt.Errorf("no message %d in chat %d", msgID, chatID)
	case !msgs[i].Out:
		return fmt.Errorf("message %d was not sent by this account, only your own messages can be edited", msgID)
	}
	// Edit a copy: chat previews handed out earlier point into the old one.
	msgs = slices.Clone(msgs)
	msgs[i].Text, msgs[i].Edited = text, true
	b.messages[chatID] = msgs
	if chat, ok := b.chat(chatID); ok && chat.Last != nil && chat.Last.ID == msgID {
		chat.Last = &msgs[i]
	}
	return nil
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
	return paint(size, size, func(x, y float64) color.NRGBA {
		t := (x + y) / (2 * size)
		if math.Hypot(x-size*0.5, y-size*0.42) < size*0.2 || math.Hypot(x-size*0.5, y-size*1.02) < size*0.42 {
			return hsl(hue+40, 0.5, 0.82) // head and shoulders
		}
		return hsl(hue+t*60, 0.65, 0.35+t*0.2)
	})
}

// The clips of the demo video sticker and video, with their stills, were made
// with ffmpeg's geq filter: a bouncing face as VP9 with transparency, the
// format of video stickers, and a sunset as H.264. The animated sticker is
// Lottie written by hand.
var (
	//go:embed media/sticker.webm
	stickerClip []byte
	//go:embed media/sticker.webp
	stickerStill []byte
	//go:embed media/video.mp4
	videoClip []byte
	//go:embed media/video.jpg
	videoStill []byte
	//go:embed media/heart.json
	heartJSON []byte
)

const heartPx = 128 // the side of the heart's still

// heart is the animated sticker as Telegram serves it: gzipped Lottie (TGS)
// with a small still.
var heart = sync.OnceValues(func() (core.Picture, error) {
	var tgs bytes.Buffer
	zw := gzip.NewWriter(&tgs)
	if _, err := zw.Write(heartJSON); err != nil {
		return core.Picture{}, err
	}
	if err := zw.Close(); err != nil {
		return core.Picture{}, err
	}
	anim, err := lottie.Parse(heartJSON)
	if err != nil {
		return core.Picture{}, err
	}
	frame, err := anim.Render(anim.In, heartPx, heartPx)
	if err != nil {
		return core.Picture{}, err
	}
	var still bytes.Buffer
	err = png.Encode(&still, frame)
	return core.Picture{Still: still.Bytes(), Clip: tgs.Bytes()}, err
})

// pictureKind tells how fixture media is drawn, from its label.
func pictureKind(media string) core.PictureKind {
	switch {
	case media == core.MediaPhoto:
		return core.PicturePhoto
	case strings.HasPrefix(media, "sticker"):
		return core.PictureSticker
	case strings.HasPrefix(media, "video"):
		return core.PictureVideo
	}
	return core.NoPicture
}

// pictureSize is the size of what Picture draws for a message, as Telegram
// sends it along with the message.
func pictureSize(media string, kind core.PictureKind) image.Point {
	switch {
	case media == "sticker ❤️":
		return image.Pt(heartPx, heartPx)
	case kind == core.PictureSticker:
		return stillSize(stickerStill)
	case kind == core.PictureVideo:
		return stillSize(videoStill)
	case kind == core.PicturePhoto:
		return image.Pt(landscapeW, landscapeH)
	}
	return image.Point{}
}

// stillSize is the size of an encoded image, zero when it cannot be read.
func stillSize(data []byte) image.Point {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return image.Point{}
	}
	return image.Pt(cfg.Width, cfg.Height)
}

// The size of the landscapes painted for photos.
const landscapeW, landscapeH = 640, 420

// Picture returns a demo sticker or the video, what was sent as a photo, or
// paints a landscape: a sky, a sun and hills, different for every message.
func (b *Backend) Picture(_ context.Context, chatID int64, msgID int) (core.Picture, error) {
	b.mu.Lock()
	sent, ok := b.photos[msgID]
	var kind core.PictureKind
	var media string
	for _, m := range b.messages[chatID] {
		if m.ID == msgID {
			kind, media = m.Picture, m.Media
		}
	}
	b.mu.Unlock()
	switch {
	case ok:
		return core.Picture{Still: sent}, nil
	case media == "sticker ❤️":
		return heart()
	case kind == core.PictureSticker:
		return core.Picture{Still: stickerStill, Clip: stickerClip}, nil
	case kind == core.PictureVideo:
		return core.Picture{Still: videoStill, Clip: videoClip}, nil
	case kind == core.NoPicture:
		return core.Picture{}, fmt.Errorf("message %d has nothing to draw", msgID)
	}
	const w, h = landscapeW, landscapeH
	hue := float64(msgID) * 67
	still, err := paint(w, h, func(x, y float64) color.NRGBA {
		hill := h*0.62 + math.Sin(x/w*5+float64(msgID))*h*0.06
		switch {
		case y > hill:
			return hsl(hue+120, 0.45, 0.32-(y-hill)/h*0.3)
		case math.Hypot(x-w*0.7, y-h*0.3) < h*0.11:
			return hsl(45, 0.95, 0.75)
		default:
			return hsl(hue+200, 0.6, 0.45+y/h*0.35)
		}
	})
	return core.Picture{Still: still}, err
}

// paint encodes a w x h PNG whose pixels come from at.
func paint(w, h int, at func(x, y float64) color.NRGBA) ([]byte, error) {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetNRGBA(x, y, at(float64(x), float64(y)))
		}
	}
	var buf bytes.Buffer
	err := png.Encode(&buf, img)
	return buf.Bytes(), err
}

func (b *Backend) Download(_ context.Context, chatID int64, msgID int, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("demo_%d_%d.txt", chatID, msgID))
	file, err := core.CreateNew(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	_, err = file.WriteString("demo media\n")
	return path, err
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
