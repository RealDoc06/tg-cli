package mtp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/telegram/message/unpack"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/RealDoc06/tg-cli/internal/config"
	"github.com/RealDoc06/tg-cli/internal/core"
)

// pageSize is the largest page Telegram serves for dialogs and history.
const pageSize = 100

// backend implements core.Backend on a connected, authorized client.
type backend struct {
	client *telegram.Client
	api    *tg.Client
	self   core.User
	peers  *peerStore
	sender *message.Sender

	// gaps orders live updates, nil when the client was opened without them.
	gaps *updates.Manager
	// onEvent receives live updates once Watch is running.
	onEvent atomic.Pointer[func(core.Event)]
	// imageSlots bounds concurrent downloads of profile pictures and photos
	// shown in the TUI.
	imageSlots chan struct{}
}

func (b *backend) Self() core.User { return b.self }

func (b *backend) Chats(ctx context.Context, q core.ChatQuery) ([]core.Chat, error) {
	req := &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}}
	if q.After != nil {
		if q.After.Last == nil {
			return nil, nil
		}
		offset, err := b.peers.input(q.After.ID)
		if err != nil {
			return nil, err
		}
		req.OffsetPeer, req.OffsetID, req.OffsetDate = offset, q.After.Last.ID, int(q.After.Last.Date.Unix())
	}

	// Pinned chats come with the first page only. Later pages can still
	// repeat a chat whose position changed in between, hence seen.
	req.ExcludePinned = q.After != nil
	seen := map[int64]bool{}
	var out []core.Chat
	for q.Limit <= 0 || len(out) < q.Limit {
		req.Limit = pageSize
		if q.Limit > 0 {
			req.Limit = min(pageSize, q.Limit-len(out))
		}
		res, err := b.api.MessagesGetDialogs(ctx, req)
		if err != nil {
			return nil, err
		}
		page, ok := res.AsModified()
		if !ok {
			break
		}
		e := tg.EntitiesFrom(page.GetUsers(), page.GetChats())
		b.peers.learn(e, b.self.ID)

		type key struct {
			chat int64
			msg  int
		}
		last := map[key]core.Message{}
		for _, raw := range page.GetMessages() {
			if m, ok := b.message(raw, e); ok {
				last[key{m.ChatID, m.ID}] = m
			}
		}

		dialogs := page.GetDialogs()
		for _, raw := range dialogs {
			d, ok := raw.(*tg.Dialog)
			if !ok {
				continue // the archive folder entry
			}
			id := chatIDOf(d.Peer)
			b.peers.markDialog(id)
			chat, known := b.peers.chat(id)
			if !known {
				chat.Title = "Unavailable chat" // banned from it, or it was deleted
			}
			chat.Unread, chat.Pinned, chat.ReadOutbox = d.UnreadCount, d.Pinned, d.ReadOutboxMaxID
			if m, ok := last[key{id, d.TopMessage}]; ok {
				chat.Last = &m
				req.OffsetID, req.OffsetDate = m.ID, int(m.Date.Unix())
			}
			if offset, err := b.peers.input(id); err == nil {
				req.OffsetPeer = offset
			}
			if !seen[id] {
				seen[id] = true
				out = append(out, chat)
			}
		}
		req.ExcludePinned = true
		if _, complete := res.(*tg.MessagesDialogs); complete || len(dialogs) < req.Limit {
			break
		}
	}
	return out, nil
}

func (b *backend) Resolve(ctx context.Context, ref string) (core.Chat, error) {
	ref = strings.TrimSpace(ref)
	for _, prefix := range []string{"https://t.me/", "http://t.me/", "t.me/"} {
		if rest, ok := strings.CutPrefix(ref, prefix); ok {
			ref = "@" + rest
		}
	}

	switch {
	case ref == "":
		return core.Chat{}, errors.New("empty chat reference")

	case strings.EqualFold(ref, "me") || strings.EqualFold(ref, "self"):
		return b.known(ctx, b.self.ID)

	case strings.HasPrefix(ref, "@"):
		name := ref[1:]
		if id, ok := b.peers.byUsername(name); ok {
			return b.known(ctx, id)
		}
		res, err := b.api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: name})
		if err != nil {
			return core.Chat{}, fmt.Errorf("resolve %s: %w", ref, err)
		}
		b.peers.learn(tg.EntitiesFrom(res.Users, res.Chats), b.self.ID)
		return b.known(ctx, chatIDOf(res.Peer))

	case strings.HasPrefix(ref, "+"):
		res, err := b.api.ContactsResolvePhone(ctx, strings.TrimPrefix(ref, "+"))
		if err != nil {
			return core.Chat{}, fmt.Errorf("resolve %s: %w", ref, err)
		}
		b.peers.learn(tg.EntitiesFrom(res.Users, res.Chats), b.self.ID)
		return b.known(ctx, chatIDOf(res.Peer))
	}

	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		return b.known(ctx, id)
	}

	// A title. Look in the cached chat list first, then refresh it once.
	matches := b.peers.byTitle(ref)
	if len(matches) == 0 {
		if _, err := b.Chats(ctx, core.ChatQuery{Limit: 3 * pageSize}); err != nil {
			return core.Chat{}, err
		}
		matches = b.peers.byTitle(ref)
	}
	switch len(matches) {
	case 0:
		return core.Chat{}, fmt.Errorf("no chat matches %q, find it with `tg chats -q <text>`", ref)
	case 1:
		return b.known(ctx, matches[0])
	}
	slices.Sort(matches)
	var list strings.Builder
	for _, id := range matches {
		chat, _ := b.peers.chat(id)
		fmt.Fprintf(&list, "\n  %d  %s", id, chat.Title)
	}
	return core.Chat{}, fmt.Errorf("%q matches several chats, use an ID:%s", ref, list.String())
}

// known returns a chat by ID, fetching the chat list once if the peer has not
// been seen by this installation yet.
func (b *backend) known(ctx context.Context, id int64) (core.Chat, error) {
	if chat, ok := b.peers.chat(id); ok {
		return chat, nil
	}
	if _, err := b.Chats(ctx, core.ChatQuery{Limit: 3 * pageSize}); err != nil {
		return core.Chat{}, err
	}
	if chat, ok := b.peers.chat(id); ok {
		return chat, nil
	}
	_, err := b.peers.input(id)
	return core.Chat{}, err
}

func (b *backend) History(ctx context.Context, chatID int64, q core.HistoryQuery) ([]core.Message, error) {
	peer, err := b.peers.input(chatID)
	if err != nil {
		return nil, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = pageSize
	}
	var out []core.Message
	offset := q.BeforeID
	for len(out) < limit {
		want := min(pageSize, limit-len(out))
		res, err := b.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer: peer, Limit: want, OffsetID: offset, MinID: q.AfterID,
		})
		if err != nil {
			return nil, err
		}
		page := b.messages(res)
		out = append(out, page...)
		if len(page) < want {
			break
		}
		offset = page[len(page)-1].ID
	}
	slices.Reverse(out)
	return out, nil
}

// messages converts an API result, keeping the server's newest first order.
func (b *backend) messages(res tg.MessagesMessagesClass) []core.Message {
	page, ok := res.AsModified()
	if !ok {
		return nil
	}
	e := tg.EntitiesFrom(page.GetUsers(), page.GetChats())
	b.peers.learn(e, b.self.ID)
	var out []core.Message
	for _, raw := range page.GetMessages() {
		if m, ok := b.message(raw, e); ok {
			out = append(out, m)
		}
	}
	return out
}

func (b *backend) Send(ctx context.Context, chatID int64, req core.SendRequest) (core.Message, error) {
	peer, err := b.peers.input(chatID)
	if err != nil {
		return core.Message{}, err
	}
	builder := &b.sender.To(peer).Builder
	if req.ReplyTo != 0 {
		builder = builder.Reply(req.ReplyTo)
	}

	sent := core.Message{ChatID: chatID, FromID: b.self.ID, From: b.self.Name, Out: true, Text: req.Text, ReplyTo: req.ReplyTo}
	var caption []styling.StyledTextOption
	if req.Text != "" {
		caption = append(caption, styling.Plain(req.Text))
	}
	var res tg.UpdatesClass
	switch {
	case req.Photo != nil:
		sent.Media, sent.Picture = core.MediaPhoto, core.PicturePhoto
		res, err = builder.Upload(message.FromBytes("photo.png", req.Photo)).Photo(ctx, caption...)
	case req.File == "":
		res, err = builder.Text(ctx, req.Text)
	case core.IsPhotoFile(req.File):
		sent.Media, sent.Picture = core.MediaPhoto, core.PicturePhoto
		res, err = builder.Upload(message.FromPath(req.File)).Photo(ctx, caption...)
	default:
		sent.Media = "file: " + filepath.Base(req.File)
		res, err = builder.Upload(message.FromPath(req.File)).File(ctx, caption...)
	}
	if sent.ID, err = unpack.MessageID(res, err); err != nil {
		return core.Message{}, err
	}
	sent.Date = time.Now()
	return sent, nil
}

func (b *backend) Edit(ctx context.Context, chatID int64, msgID int, text string) error {
	peer, err := b.peers.input(chatID)
	if err != nil {
		return err
	}
	_, err = b.sender.To(peer).Edit(msgID).Text(ctx, text)
	return err
}

func (b *backend) Press(ctx context.Context, chatID int64, msgID int, data string) (core.BotAnswer, error) {
	peer, err := b.peers.input(chatID)
	if err != nil {
		return core.BotAnswer{}, err
	}
	res, err := b.api.MessagesGetBotCallbackAnswer(ctx, &tg.MessagesGetBotCallbackAnswerRequest{
		Peer: peer, MsgID: msgID, Data: []byte(data),
	})
	if tgerr.Is(err, "BOT_RESPONSE_TIMEOUT") {
		return core.BotAnswer{}, nil // the bot got it, and may answer by editing its message
	}
	if err != nil {
		return core.BotAnswer{}, err
	}
	return core.BotAnswer{Text: res.Message, URL: res.URL}, nil
}

func (b *backend) MarkRead(ctx context.Context, chatID int64, maxID int) error {
	peer, err := b.peers.input(chatID)
	if err != nil {
		return err
	}
	if channel, ok := peer.(*tg.InputPeerChannel); ok {
		_, err = b.api.ChannelsReadHistory(ctx, &tg.ChannelsReadHistoryRequest{
			Channel: &tg.InputChannel{ChannelID: channel.ChannelID, AccessHash: channel.AccessHash}, MaxID: maxID,
		})
		return err
	}
	_, err = b.api.MessagesReadHistory(ctx, &tg.MessagesReadHistoryRequest{Peer: peer, MaxID: maxID})
	return err
}

// CreateGroup creates a supergroup, the only kind of group that can exist
// with a single member.
func (b *backend) CreateGroup(ctx context.Context, title string) (core.Chat, error) {
	res, err := b.api.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{Megagroup: true, Title: title})
	if err != nil {
		return core.Chat{}, err
	}
	var created []tg.ChatClass
	switch u := res.(type) {
	case *tg.Updates:
		created = u.Chats
	case *tg.UpdatesCombined:
		created = u.Chats
	}
	e := tg.EntitiesFrom(nil, created)
	b.peers.learn(e, b.self.ID)
	for id := range e.Channels {
		b.peers.markDialog(channelChatID(id))
		chat, _ := b.peers.chat(channelChatID(id))
		return chat, nil
	}
	return core.Chat{}, errors.New("Telegram did not return the new group")
}

func (b *backend) Search(ctx context.Context, q core.SearchQuery) ([]core.Message, error) {
	limit := q.Limit
	if limit <= 0 || limit > pageSize {
		limit = pageSize
	}
	var res tg.MessagesMessagesClass
	var err error
	if q.ChatID == 0 {
		res, err = b.api.MessagesSearchGlobal(ctx, &tg.MessagesSearchGlobalRequest{
			Q: q.Text, Filter: &tg.InputMessagesFilterEmpty{}, OffsetPeer: &tg.InputPeerEmpty{}, Limit: limit,
		})
	} else {
		var peer tg.InputPeerClass
		if peer, err = b.peers.input(q.ChatID); err != nil {
			return nil, err
		}
		res, err = b.api.MessagesSearch(ctx, &tg.MessagesSearchRequest{
			Peer: peer, Q: q.Text, Filter: &tg.InputMessagesFilterEmpty{}, Limit: limit,
		})
	}
	if err != nil {
		return nil, err
	}
	out := b.messages(res)
	for i := range out {
		if chat, ok := b.peers.chat(out[i].ChatID); ok {
			out[i].Chat = chat.Title
		}
	}
	slices.Reverse(out)
	return out, nil
}

func (b *backend) Avatar(ctx context.Context, chat core.Chat) ([]byte, error) {
	if chat.PhotoID == 0 {
		return nil, fmt.Errorf("chat %d has no photo", chat.ID)
	}
	path := filepath.Join(config.CacheDir(), "avatars", fmt.Sprintf("%d.jpg", chat.PhotoID))
	if data, err := os.ReadFile(path); err == nil {
		return data, nil
	}
	peer, err := b.peers.input(chat.ID)
	if err != nil {
		return nil, err
	}

	data, err := b.fetchImage(ctx, &tg.InputPeerPhotoFileLocation{Peer: peer, PhotoID: chat.PhotoID})
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
		_ = os.WriteFile(path, data, 0o600) // the cache is best effort
	}
	return data, nil
}

// photoDisplaySize is the smallest side, in pixels, Picture looks for:
// enough for a sharp picture across half a terminal on a high density screen.
const photoDisplaySize = 800

// Clips past these limits show only their still: they cost a download and
// frames the terminal keeps in memory. A GIF plays whatever its length.
const (
	clipMaxBytes   = 4 << 20
	clipMaxSeconds = 6
)

func (b *backend) Picture(ctx context.Context, chatID int64, msgID int) (core.Picture, error) {
	media, err := b.media(ctx, chatID, msgID)
	if err != nil {
		return core.Picture{}, err
	}
	switch m := media.(type) {
	case *tg.MessageMediaPhoto:
		if m.Spoiler {
			return core.Picture{}, errors.New("the photo is behind a spoiler")
		}
		photo, ok := m.Photo.(*tg.Photo)
		if !ok {
			return core.Picture{}, errors.New("photo is no longer available")
		}
		still, err := b.fetchImage(ctx, photoFile(photo, thumbSize(photo.Sizes, photoDisplaySize)))
		return core.Picture{Still: still}, err
	case *tg.MessageMediaDocument:
		if m.Spoiler {
			return core.Picture{}, errors.New("the file is behind a spoiler")
		}
		if doc, ok := m.Document.(*tg.Document); ok {
			return b.documentPicture(ctx, doc)
		}
	}
	return core.Picture{}, fmt.Errorf("message %d has nothing to draw", msgID)
}

// documentPicture fetches the still of a sticker, GIF or video: the file
// itself for a static sticker, its thumbnail otherwise, plus the file as a
// clip when it is short enough to play.
func (b *backend) documentPicture(ctx context.Context, doc *tg.Document) (core.Picture, error) {
	at := func(size string) *tg.InputDocumentFileLocation {
		return &tg.InputDocumentFileLocation{ID: doc.ID, AccessHash: doc.AccessHash, FileReference: doc.FileReference, ThumbSize: size}
	}
	if doc.MimeType == "image/webp" {
		still, err := b.fetchImage(ctx, at(""))
		return core.Picture{Still: still}, err
	}
	thumb := thumbSize(doc.Thumbs, 0)
	if thumb == "" {
		return core.Picture{}, errors.New("file has no preview")
	}
	still, err := b.fetchImage(ctx, at(thumb))
	if err != nil {
		return core.Picture{}, err
	}
	pic := core.Picture{Still: still}
	if playable(doc) {
		pic.Clip, _ = b.fetchImage(ctx, at("")) // the still stands in without it
	}
	return pic, nil
}

// playable reports whether a document is a clip to play in place of its
// still: an animated or video sticker, a GIF, or a short video.
func playable(doc *tg.Document) bool {
	if doc.Size > clipMaxBytes {
		return false
	}
	a := attrsOf(doc)
	switch {
	case a.sticker != nil:
		return doc.MimeType == "video/webm" || doc.MimeType == "application/x-tgsticker"
	case a.animated:
		return true
	}
	// Round video messages keep their still: they talk, and play muted.
	return a.video != nil && !a.video.RoundMessage && a.video.Duration <= clipMaxSeconds
}

// fetchImage downloads a small file into memory, a few at a time.
func (b *backend) fetchImage(ctx context.Context, location tg.InputFileLocationClass) ([]byte, error) {
	select {
	case b.imageSlots <- struct{}{}:
		defer func() { <-b.imageSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var buf bytes.Buffer
	if _, err := b.client.Download(location).Stream(ctx, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// thumbSize picks the smallest size whose longest side reaches want pixels,
// or the largest one when none does, and returns its type. want 0 means the
// largest. Sizes are listed from small to large. It is "" when there is no
// size to download.
func thumbSize(sizes []tg.PhotoSizeClass, want int) string {
	var size string
	for _, s := range sizes {
		var typ string
		var w, h int
		switch s := s.(type) {
		case *tg.PhotoSize:
			typ, w, h = s.Type, s.W, s.H
		case *tg.PhotoSizeProgressive:
			typ, w, h = s.Type, s.W, s.H
		default:
			continue // stripped and cached sizes are inlined thumbnails
		}
		size = typ
		if want > 0 && max(w, h) >= want {
			break
		}
	}
	return size
}

// photoFile is the location of one size of a photo.
func photoFile(photo *tg.Photo, size string) *tg.InputPhotoFileLocation {
	return &tg.InputPhotoFileLocation{ID: photo.ID, AccessHash: photo.AccessHash, FileReference: photo.FileReference, ThumbSize: size}
}

// media fetches the attachment of one message.
func (b *backend) media(ctx context.Context, chatID int64, msgID int) (tg.MessageMediaClass, error) {
	peer, err := b.peers.input(chatID)
	if err != nil {
		return nil, err
	}
	ids := []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}}
	var res tg.MessagesMessagesClass
	if channel, ok := peer.(*tg.InputPeerChannel); ok {
		res, err = b.api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: channel.ChannelID, AccessHash: channel.AccessHash}, ID: ids,
		})
	} else {
		res, err = b.api.MessagesGetMessages(ctx, ids)
	}
	if err != nil {
		return nil, err
	}
	page, ok := res.AsModified()
	if !ok || len(page.GetMessages()) == 0 {
		return nil, fmt.Errorf("message %d not found", msgID)
	}
	msg, ok := page.GetMessages()[0].(*tg.Message)
	if !ok {
		return nil, fmt.Errorf("message %d not found", msgID)
	}
	return msg.Media, nil
}

func (b *backend) Download(ctx context.Context, chatID int64, msgID int, dir string) (string, error) {
	media, err := b.media(ctx, chatID, msgID)
	if err != nil {
		return "", err
	}

	var location tg.InputFileLocationClass
	var name string
	switch media := media.(type) {
	case *tg.MessageMediaPhoto:
		photo, ok := media.Photo.(*tg.Photo)
		if !ok {
			return "", errors.New("photo is no longer available")
		}
		location = photoFile(photo, thumbSize(photo.Sizes, 0))
		name = fmt.Sprintf("photo_%d.jpg", msgID)
	case *tg.MessageMediaDocument:
		doc, ok := media.Document.(*tg.Document)
		if !ok {
			return "", errors.New("file is no longer available")
		}
		location = &tg.InputDocumentFileLocation{ID: doc.ID, AccessHash: doc.AccessHash, FileReference: doc.FileReference}
		name = fmt.Sprintf("file_%d", msgID)
		for _, attr := range doc.Attributes {
			if f, ok := attr.(*tg.DocumentAttributeFilename); ok {
				name = filepath.Base(f.FileName)
			}
		}
	default:
		return "", fmt.Errorf("message %d has no downloadable media", msgID)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	file, err := core.CreateNew(path)
	if err != nil {
		return "", err
	}
	_, err = b.client.Download(location).Stream(ctx, file)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path) // nothing half written stays behind
		return "", err
	}
	return path, nil
}

func (b *backend) Watch(ctx context.Context, fn func(core.Event)) error {
	if b.gaps == nil {
		return errors.New("client was opened without updates")
	}
	b.onEvent.Store(&fn)
	// OnStart runs once the update state is loaded and before any update is
	// handed out, which is exactly the EventReady contract. Updates queue up
	// behind it in a small buffer, hence fn must not linger.
	err := b.gaps.Run(ctx, b.api, b.self.ID, updates.AuthOptions{
		OnStart: func(context.Context) { fn(core.Event{Kind: core.EventReady}) },
	})
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// emit forwards a live message to the Watch callback.
func (b *backend) emit(kind core.EventKind, e tg.Entities, raw tg.MessageClass) {
	b.peers.learn(e, b.self.ID)
	msg, ok := b.message(raw, e)
	fn := b.onEvent.Load()
	if !ok || fn == nil {
		return
	}
	chat, _ := b.peers.chat(msg.ChatID)
	chat.ID = msg.ChatID
	(*fn)(core.Event{Kind: kind, Chat: chat, Message: msg})
}

// emitRead forwards that the other side of a chat read the account's
// messages up to maxID.
func (b *backend) emitRead(chatID int64, maxID int) {
	fn := b.onEvent.Load()
	if fn == nil {
		return
	}
	(*fn)(core.Event{Kind: core.EventRead, Chat: core.Chat{ID: chatID, ReadOutbox: maxID}})
}

func newSender(api *tg.Client) *message.Sender {
	return message.NewSender(api).WithUploader(uploader.NewUploader(api))
}
