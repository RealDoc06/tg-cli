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
			Edited: m.EditDate != 0 && !m.EditHide,
		}
		out.Media, out.Picture = mediaSummary(m.Media)
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

// mediaSummary names the attachment of a message in a few words and tells
// how it can be drawn. Media behind a spoiler is not drawn.
func mediaSummary(media tg.MessageMediaClass) (string, core.PictureKind) {
	switch m := media.(type) {
	case nil, *tg.MessageMediaEmpty, *tg.MessageMediaWebPage:
		return "", core.NoPicture
	case *tg.MessageMediaPhoto:
		if m.Spoiler {
			return core.MediaPhoto, core.NoPicture
		}
		return core.MediaPhoto, core.PicturePhoto
	case *tg.MessageMediaDocument:
		doc, ok := m.Document.(*tg.Document)
		if !ok {
			return "file", core.NoPicture
		}
		summary, kind := documentSummary(doc)
		if m.Spoiler {
			kind = core.NoPicture
		}
		return summary, kind
	case *tg.MessageMediaGeo, *tg.MessageMediaGeoLive, *tg.MessageMediaVenue:
		return "location", core.NoPicture
	case *tg.MessageMediaContact:
		return "contact: " + m.FirstName + " " + m.PhoneNumber, core.NoPicture
	case *tg.MessageMediaPoll:
		return "poll", core.NoPicture
	case *tg.MessageMediaDice:
		return fmt.Sprintf("dice %s %d", m.Emoticon, m.Value), core.NoPicture
	case *tg.MessageMediaStory:
		return "story", core.NoPicture
	}
	return "media", core.NoPicture
}

// docAttrs gathers the attributes of a document, which come in any order.
type docAttrs struct {
	sticker  *tg.DocumentAttributeSticker
	animated bool
	video    *tg.DocumentAttributeVideo
	audio    *tg.DocumentAttributeAudio
	name     string
}

func attrsOf(doc *tg.Document) docAttrs {
	var a docAttrs
	for _, attr := range doc.Attributes {
		switch attr := attr.(type) {
		case *tg.DocumentAttributeSticker:
			a.sticker = attr
		case *tg.DocumentAttributeAnimated:
			a.animated = true
		case *tg.DocumentAttributeVideo:
			a.video = attr
		case *tg.DocumentAttributeAudio:
			a.audio = attr
		case *tg.DocumentAttributeFilename:
			a.name = attr.FileName
		}
	}
	return a
}

// documentSummary names a document by what it is: a video sticker also
// carries video attributes, a GIF is a video flagged as animated.
func documentSummary(doc *tg.Document) (string, core.PictureKind) {
	a := attrsOf(doc)
	switch {
	case a.sticker != nil:
		return "sticker " + a.sticker.Alt, core.PictureSticker
	case a.animated:
		return "gif", core.PicturePhoto
	case a.video != nil && a.video.RoundMessage:
		return "video message " + clock(a.video.Duration), core.PictureVideo
	case a.video != nil:
		return "video " + clock(a.video.Duration), core.PictureVideo
	case a.audio != nil && a.audio.Voice:
		return "voice " + clock(float64(a.audio.Duration)), core.NoPicture
	case a.audio != nil:
		return "audio " + clock(float64(a.audio.Duration)), core.NoPicture
	case a.name != "":
		return "file: " + a.name, core.NoPicture
	}
	return "file", core.NoPicture
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
