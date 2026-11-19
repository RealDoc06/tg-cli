// Package core holds the domain types shared by the CLI, the TUI and the
// backends, plus the Backend interface that both front ends are written against.
package core

import (
	"context"
	"errors"
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

// SendRequest is a message to send. File is an optional path to attach, in
// which case Text becomes the caption.
type SendRequest struct {
	Text    string
	ReplyTo int
	File    string
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
	MarkRead(ctx context.Context, chatID int64, maxID int) error
	Search(ctx context.Context, q SearchQuery) ([]Message, error)
	// Avatar returns the encoded profile photo of a chat (JPEG or PNG).
	Avatar(ctx context.Context, chat Chat) ([]byte, error)
	// Download saves the media of a message into dir and returns the file path.
	Download(ctx context.Context, chatID int64, msgID int, dir string) (string, error)
	// Watch blocks and reports live events until ctx is done.
	Watch(ctx context.Context, fn func(Event)) error
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
