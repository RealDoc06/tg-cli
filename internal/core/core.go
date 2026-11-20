// Package core holds the domain types shared by the CLI, the TUI and the
// backends, plus the Backend interface that both front ends are written against.
package core

import (
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ChatKind tells what sits on the other side of a chat.
type ChatKind string

const (
	KindUser    ChatKind = "user"
	KindBot     ChatKind = "bot"
	KindGroup   ChatKind = "group"
	KindChannel ChatKind = "channel"
)

// Chat is a dialog. IDs follow the Bot API convention: users are positive,
// basic groups are negative, channels and supergroups are -100xxxxxxxxxx.
type Chat struct {
	ID       int64    `json:"id"`
	Kind     ChatKind `json:"kind"`
	Title    string   `json:"title"`
	Username string   `json:"username,omitempty"`
	Unread   int      `json:"unread"`
	Pinned   bool     `json:"pinned,omitempty"`
	Last     *Message `json:"last,omitempty"`
	// PhotoID identifies the current profile photo, 0 when there is none.
	PhotoID int64 `json:"-"`
}

// Message is one message of a chat. Media and service messages carry a short
// human readable summary in Media ("photo", "voice 0:12", "file: a.pdf").
type Message struct {
	ID     int   `json:"id"`
	ChatID int64 `json:"chat_id"`
	// Chat is the chat title, only set where messages of several chats mix.
	Chat    string    `json:"chat,omitempty"`
	FromID  int64     `json:"from_id,omitempty"`
	From    string    `json:"from"`
	Out     bool      `json:"out,omitempty"`
	Date    time.Time `json:"date"`
	Text    string    `json:"text"`
	Media   string    `json:"media,omitempty"`
	ReplyTo int       `json:"reply_to,omitempty"`
	Edited  bool      `json:"edited,omitempty"`
	Service bool      `json:"service,omitempty"`
	// Picture tells whether and how the TUI can draw the media.
	Picture PictureKind `json:"-"`
	// PictureSize is the media's size in pixels, zero when unknown. The TUI
	// sets room aside for the picture with it, so nothing moves when it loads.
	PictureSize image.Point `json:"-"`
	// Buttons are a bot's inline keyboard, row by row.
	Buttons [][]Button `json:"-"`
}

// ButtonKind tells what pressing a button of a bot's keyboard does.
type ButtonKind uint8

const (
	// ButtonOther only works in Telegram's own apps, like a game or a payment.
	ButtonOther ButtonKind = iota
	// ButtonCallback sends its Value to the bot, see Backend.Press.
	ButtonCallback
	// ButtonURL opens the link in its Value.
	ButtonURL
	// ButtonCopy copies its Value.
	ButtonCopy
)

// Button is one button of a bot's inline keyboard.
type Button struct {
	Text  string
	Kind  ButtonKind
	Value string
}

// BotAnswer is what a bot replies to a pressed button: a notice to show and
// a link to open. Either can be empty, a bot often answers by editing its
// message instead.
type BotAnswer struct {
	Text string
	URL  string
}

// MediaPhoto is the Media of a message that carries a photo.
const MediaPhoto = "photo"

// PictureKind tells how the media of a message is drawn.
type PictureKind uint8

const (
	NoPicture PictureKind = iota
	// PicturePhoto is a photo or a GIF, drawn in place of its label.
	PicturePhoto
	// PictureSticker is drawn smaller than a photo, in place of its label.
	PictureSticker
	// PictureVideo is drawn under its label, which keeps the duration.
	PictureVideo
)

// Picture is what can be drawn of a message's media. Still is an encoded
// image (JPEG, PNG or WebP). Clip, when set, is a short muted video (MP4 or
// WebM) or an animated sticker (TGS) that plays in its place; the still
// stands in when it cannot.
type Picture struct {
	Still []byte
	Clip  []byte
}

// User is the logged in account.
type User struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username,omitempty"`
	Phone    string `json:"phone,omitempty"`
}

// ChatQuery pages through the chat list. After is the last chat of the
// previous page.
type ChatQuery struct {
	Limit int
	After *Chat
}

// HistoryQuery selects a slice of a chat history. BeforeID and AfterID are
// exclusive message ID bounds, 0 means unbounded.
type HistoryQuery struct {
	Limit    int
	BeforeID int
	AfterID  int
}

// SendRequest is a message to send. File is an optional path to attach, or
// Photo an encoded image (PNG or JPEG) to send as a photo, in which case Text
// becomes the caption.
type SendRequest struct {
	Text    string
	ReplyTo int
	File    string
	Photo   []byte
}

// IsPhotoFile reports whether a file is sent as a photo rather than as a
// document, judging by its extension.
func IsPhotoFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".webp":
		return true
	}
	return false
}

// SearchQuery searches messages in one chat, or everywhere when ChatID is 0.
type SearchQuery struct {
	Text   string
	ChatID int64
	Limit  int
}

// EventKind tells what happened in an Event.
type EventKind string

const (
	EventMessage EventKind = "message"
	EventEdit    EventKind = "edit"
	// EventReady is sent once, when the subscription is live: every message
	// from then on is reported. It carries no chat or message.
	EventReady EventKind = "ready"
)

// Event is a live update. Chat describes the chat the message belongs to, so
// consumers can show chats they have not listed yet.
type Event struct {
	Kind    EventKind `json:"kind"`
	Chat    Chat      `json:"chat"`
	Message Message   `json:"message"`
}

// ErrNotLoggedIn is returned by backends when there is no usable session.
var ErrNotLoggedIn = errors.New("not logged in, run `tg login` first")

// Backend is everything the front ends need from Telegram. Message slices are
// always ordered oldest first.
type Backend interface {
	Self() User
	Chats(ctx context.Context, q ChatQuery) ([]Chat, error)
	// Resolve turns a user supplied reference into a chat: a numeric ID,
	// "me", @username, +phone or a chat title.
	Resolve(ctx context.Context, ref string) (Chat, error)
	History(ctx context.Context, chatID int64, q HistoryQuery) ([]Message, error)
	Send(ctx context.Context, chatID int64, req SendRequest) (Message, error)
	// Edit replaces the text, or the caption, of a message. Telegram decides
	// what may be edited, which in a channel can include other admins'
	// posts: front ends offer it only for messages the account sent.
	Edit(ctx context.Context, chatID int64, msgID int, text string) error
	MarkRead(ctx context.Context, chatID int64, maxID int) error
	// Press presses a ButtonCallback of message msgID, data being its Value.
	Press(ctx context.Context, chatID int64, msgID int, data string) (BotAnswer, error)
	// CreateGroup makes a new group with only the account in it.
	CreateGroup(ctx context.Context, title string) (Chat, error)
	Search(ctx context.Context, q SearchQuery) ([]Message, error)
	// Avatar returns the encoded profile photo of a chat (JPEG or PNG).
	Avatar(ctx context.Context, chat Chat) ([]byte, error)
	// Picture returns what to draw for the media of a message, at a size
	// meant for display rather than the original. It is only asked for
	// messages whose Picture is not NoPicture.
	Picture(ctx context.Context, chatID int64, msgID int) (Picture, error)
	// Download saves the media of a message into dir and returns the file path.
	Download(ctx context.Context, chatID int64, msgID int, dir string) (string, error)
	// Watch blocks and reports live events until ctx is done, starting with
	// an EventReady. fn must return quickly: nothing else is delivered, and
	// requests to the backend may stall, while it runs.
	Watch(ctx context.Context, fn func(Event)) error
}

// CreateNew creates the file a download is saved to. The name comes from
// whoever sent the file, so it may never replace a file that is already
// there: creating it is refused instead, atomically.
func CreateNew(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if os.IsExist(err) {
		return nil, fmt.Errorf("%s already exists, pick another directory with -o", path)
	}
	return file, err
}

// Preview is the one line summary of a message used in chat lists.
func (m Message) Preview() string {
	switch {
	case m.Media != "" && m.Text != "":
		return "[" + m.Media + "] " + m.Text
	case m.Media != "":
		return "[" + m.Media + "]"
	default:
		return m.Text
	}
}
