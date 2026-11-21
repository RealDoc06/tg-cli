package mtp

import (
	"fmt"
	"image"
	"time"
	"unicode/utf16"

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
		out.Media, out.Picture, out.PictureSize = mediaSummary(m.Media)
		if markup, ok := m.ReplyMarkup.(*tg.ReplyInlineMarkup); ok {
			for _, row := range markup.Rows {
				buttons := make([]core.Button, len(row.Buttons))
				for i, button := range row.Buttons {
					buttons[i] = buttonOf(button)
				}
				out.Buttons = append(out.Buttons, buttons)
			}
		}
		out.Links = append(textLinks(m.Message, m.Entities), core.ButtonLinks(out.Buttons)...)
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

// textLinks lists the links hidden behind words of a message's text. Telegram
// places entities in UTF-16 code units, so the text is sliced in those.
func textLinks(text string, entities []tg.MessageEntityClass) []core.Link {
	var out []core.Link
	var units []uint16
	for _, entity := range entities {
		link, ok := entity.(*tg.MessageEntityTextURL)
		if !ok {
			continue
		}
		if units == nil {
			units = utf16.Encode([]rune(text))
		}
		start := min(max(link.Offset, 0), len(units))
		end := min(max(link.Offset+link.Length, start), len(units))
		out = append(out, core.Link{Text: string(utf16.Decode(units[start:end])), URL: link.URL})
	}
	return out
}

// buttonOf converts a button of an inline keyboard. A callback button that
// asks for the account's password is left to Telegram's own apps.
func buttonOf(button tg.KeyboardInlineButton) core.Button {
	out := core.Button{Text: button.Text}
	switch t := button.Type.(type) {
	case *tg.InlineButtonTypeCallback:
		if !t.RequiresPassword {
			out.Kind, out.Value = core.ButtonCallback, string(t.Data)
		}
	case *tg.InlineButtonTypeURL:
		out.Kind, out.Value = core.ButtonURL, t.URL
	case *tg.InlineButtonTypeURLAuth:
		// A link that can log the account in to the site: opened as a plain link.
		out.Kind, out.Value = core.ButtonURL, t.URL
	case *tg.InlineButtonTypeCopy:
		out.Kind, out.Value = core.ButtonCopy, t.CopyText
	}
	return out
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

// mediaSummary names the attachment of a message in a few words, tells how
// it can be drawn and its size in pixels when known. Media behind a spoiler
// is not drawn.
func mediaSummary(media tg.MessageMediaClass) (string, core.PictureKind, image.Point) {
	switch m := media.(type) {
	case nil, *tg.MessageMediaEmpty, *tg.MessageMediaWebPage:
		return "", core.NoPicture, image.Point{}
	case *tg.MessageMediaPhoto:
		if m.Spoiler {
			return core.MediaPhoto, core.NoPicture, image.Point{}
		}
		var size image.Point
		if photo, ok := m.Photo.(*tg.Photo); ok {
			size = largestSize(photo.Sizes)
		}
		return core.MediaPhoto, core.PicturePhoto, size
	case *tg.MessageMediaDocument:
		doc, ok := m.Document.(*tg.Document)
		if !ok {
			return "file", core.NoPicture, image.Point{}
		}
		summary, kind := documentSummary(doc)
		if m.Spoiler {
			kind = core.NoPicture
		}
		return summary, kind, attrsOf(doc).size
	case *tg.MessageMediaGeo, *tg.MessageMediaGeoLive, *tg.MessageMediaVenue:
		return "location", core.NoPicture, image.Point{}
	case *tg.MessageMediaContact:
		return "contact: " + m.FirstName + " " + m.PhoneNumber, core.NoPicture, image.Point{}
	case *tg.MessageMediaPoll:
		return "poll", core.NoPicture, image.Point{}
	case *tg.MessageMediaDice:
		return fmt.Sprintf("dice %s %d", m.Emoticon, m.Value), core.NoPicture, image.Point{}
	case *tg.MessageMediaStory:
		return "story", core.NoPicture, image.Point{}
	}
	return "media", core.NoPicture, image.Point{}
}

// largestSize is the size of the largest version of a photo, which is listed
// last. Stripped and cached sizes are inlined thumbnails, without one.
func largestSize(sizes []tg.PhotoSizeClass) image.Point {
	var size image.Point
	for _, s := range sizes {
		switch s := s.(type) {
		case *tg.PhotoSize:
			size = image.Pt(s.W, s.H)
		case *tg.PhotoSizeProgressive:
			size = image.Pt(s.W, s.H)
		}
	}
	return size
}

// docAttrs gathers the attributes of a document, which come in any order.
type docAttrs struct {
	sticker  *tg.DocumentAttributeSticker
	animated bool
	video    *tg.DocumentAttributeVideo
	audio    *tg.DocumentAttributeAudio
	name     string
	size     image.Point // of a picture or a video, zero when not given
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
			a.size = image.Pt(attr.W, attr.H)
		case *tg.DocumentAttributeImageSize:
			// A video sticker carries both: either gives its shape.
			if a.size == (image.Point{}) {
				a.size = image.Pt(attr.W, attr.H)
			}
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
