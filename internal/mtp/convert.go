package mtp

import (
	"fmt"
	"time"

	"github.com/gotd/td/tg"

	"github.com/RealDoc06/tg-cli/internal/core"
)

// message converts a Telegram message. It reports false for empty messages.
func (b *backend) message(raw tg.MessageClass, e tg.Entities) (core.Message, bool) {
	switch m := raw.(type) {
	case *tg.Message:
		out := core.Message{
			ID: m.ID, ChatID: chatIDOf(m.PeerID), Out: m.Out,
			Date: time.Unix(int64(m.Date), 0), Text: m.Message,
			Media: mediaSummary(m.Media), Edited: m.EditDate != 0 && !m.EditHide,
		}
		if reply, ok := m.ReplyTo.(*tg.MessageReplyHeader); ok && !reply.ForumTopic {
			out.ReplyTo = reply.ReplyToMsgID
		}
		out.FromID, out.From = b.author(m.FromID, m.PeerID, m.Out, e)
		// Saved Messages does not flag the account's own messages as outgoing.
		out.Out = out.Out || out.FromID == b.self.ID
		return out, true
	case *tg.MessageService:
		out := core.Message{
			ID: m.ID, ChatID: chatIDOf(m.PeerID), Out: m.Out,
			Date: time.Unix(int64(m.Date), 0), Text: actionSummary(m.Action), Service: true,
		}
		out.FromID, out.From = b.author(m.FromID, m.PeerID, m.Out, e)
		return out, true
	}
	return core.Message{}, false
}

// author works out who wrote a message. Private chats and channel posts omit
// the sender, which is then implied by the chat and the direction.
func (b *backend) author(from, chat tg.PeerClass, out bool, e tg.Entities) (int64, string) {
	if from == nil {
		from = chat
		if _, private := chat.(*tg.PeerUser); private && out {
			return b.self.ID, b.self.Name
		}
	}
	id := chatIDOf(from)
	if id == b.self.ID {
		return id, b.self.Name
	}
	switch p := from.(type) {
	case *tg.PeerUser:
		if u, ok := e.Users[p.UserID]; ok {
			return id, userName(u)
		}
	case *tg.PeerChat:
		if c, ok := e.Chats[p.ChatID]; ok {
			return id, c.Title
		}
	case *tg.PeerChannel:
		if c, ok := e.Channels[p.ChannelID]; ok {
			return id, c.Title
		}
	}
	if p, ok := b.peers.get(id); ok {
		return id, p.Title
	}
	return id, fmt.Sprint(id)
}

// mediaSummary names the attachment of a message in a few words.
func mediaSummary(media tg.MessageMediaClass) string {
	switch m := media.(type) {
	case nil, *tg.MessageMediaEmpty, *tg.MessageMediaWebPage:
		return ""
	case *tg.MessageMediaPhoto:
		return core.MediaPhoto
	case *tg.MessageMediaDocument:
		doc, ok := m.Document.(*tg.Document)
		if !ok {
			return "file"
		}
		return documentSummary(doc)
	case *tg.MessageMediaGeo, *tg.MessageMediaGeoLive, *tg.MessageMediaVenue:
		return "location"
	case *tg.MessageMediaContact:
		return "contact: " + m.FirstName + " " + m.PhoneNumber
	case *tg.MessageMediaPoll:
		return "poll"
	case *tg.MessageMediaDice:
		return fmt.Sprintf("dice %s %d", m.Emoticon, m.Value)
	case *tg.MessageMediaStory:
		return "story"
	}
	return "media"
}

func documentSummary(doc *tg.Document) string {
	summary := "file"
	for _, attr := range doc.Attributes {
		switch a := attr.(type) {
		case *tg.DocumentAttributeSticker:
			return "sticker " + a.Alt
		case *tg.DocumentAttributeAnimated:
			return "gif"
		case *tg.DocumentAttributeVideo:
			kind := "video"
			if a.RoundMessage {
				kind = "video message"
			}
			return kind + " " + clock(a.Duration)
		case *tg.DocumentAttributeAudio:
			if a.Voice {
				return "voice " + clock(float64(a.Duration))
			}
			return "audio " + clock(float64(a.Duration))
		case *tg.DocumentAttributeFilename:
			summary = "file: " + a.FileName
		}
	}
	return summary
}

func clock(seconds float64) string {
	s := int(seconds)
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// actionSummary describes a service message, phrased to follow the author's
// name ("Alice joined the group").
func actionSummary(action tg.MessageActionClass) string {
	switch a := action.(type) {
	case *tg.MessageActionChatCreate:
		return "created the group " + a.Title
	case *tg.MessageActionChannelCreate:
		return "created " + a.Title
	case *tg.MessageActionChatEditTitle:
		return "renamed the chat to " + a.Title
	case *tg.MessageActionChatEditPhoto:
		return "changed the chat photo"
	case *tg.MessageActionChatAddUser, *tg.MessageActionChatJoinedByLink, *tg.MessageActionChatJoinedByRequest:
		return "joined the group"
	case *tg.MessageActionChatDeleteUser:
		return "left the group"
	case *tg.MessageActionPinMessage:
		return "pinned a message"
	case *tg.MessageActionPhoneCall:
		if a.Video {
			return "video call"
		}
		return "call"
	case *tg.MessageActionContactSignUp:
		return "joined Telegram"
	}
	return "service message"
}
