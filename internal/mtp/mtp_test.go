package mtp

import (
	"path/filepath"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/RealDoc06/tg-cli/internal/core"
)

func testStore(t *testing.T) *peerStore {
	t.Helper()
	return loadPeers(filepath.Join(t.TempDir(), "peers.json"))
}

// Chat IDs are what users and agents pass back in, so every kind of peer has
// to survive the trip to an ID and back to an addressable peer.
func TestChatIDsRoundTrip(t *testing.T) {
	s := testStore(t)
	s.learn(tg.Entities{
		Users:    map[int64]*tg.User{7: {ID: 7, AccessHash: 70, FirstName: "Ann"}},
		Chats:    map[int64]*tg.Chat{8: {ID: 8, Title: "Group"}},
		Channels: map[int64]*tg.Channel{9: {ID: 9, AccessHash: 90, Title: "News", Broadcast: true}},
	}, 0)

	cases := []struct {
		peer tg.PeerClass
		id   int64
		want tg.InputPeerClass
		kind core.ChatKind
	}{
		{&tg.PeerUser{UserID: 7}, 7, &tg.InputPeerUser{UserID: 7, AccessHash: 70}, core.KindUser},
		{&tg.PeerChat{ChatID: 8}, -8, &tg.InputPeerChat{ChatID: 8}, core.KindGroup},
		{&tg.PeerChannel{ChannelID: 9}, -1000000000009, &tg.InputPeerChannel{ChannelID: 9, AccessHash: 90}, core.KindChannel},
	}
	for _, c := range cases {
		id := chatIDOf(c.peer)
		if id != c.id {
			t.Errorf("chatIDOf(%v) = %d, want %d", c.peer, id, c.id)
		}
		got, err := s.input(id)
		if err != nil || got.String() != c.want.String() {
			t.Errorf("input(%d) = %v, %v, want %v", id, got, err, c.want)
		}
		if chat, _ := s.chat(id); chat.Kind != c.kind {
			t.Errorf("chat(%d).Kind = %q, want %q", id, chat.Kind, c.kind)
		}
	}
	if _, err := s.input(12345); err == nil {
		t.Error("an unknown chat must not be addressable")
	}
}

// A "min" sighting of a user carries no usable access hash and must not
// erase the one learned before.
func TestMinPeerKeepsAccessHash(t *testing.T) {
	s := testStore(t)
	s.learn(tg.Entities{Users: map[int64]*tg.User{7: {ID: 7, AccessHash: 70, FirstName: "Ann"}}}, 0)
	s.learn(tg.Entities{Users: map[int64]*tg.User{7: {ID: 7, Min: true, FirstName: "Annie"}}}, 0)

	p, _ := s.get(7)
	if p.Hash != 70 || p.Title != "Annie" {
		t.Fatalf("peer = %+v", p)
	}
}

// Sending by title must never reach someone outside the chat list, and an
// exact title wins over titles that merely contain it.
func TestByTitleOnlyMatchesChatList(t *testing.T) {
	s := testStore(t)
	s.learn(tg.Entities{Users: map[int64]*tg.User{
		1: {ID: 1, FirstName: "Sam"},
		2: {ID: 2, FirstName: "Sam", LastName: "Jones"},
		3: {ID: 3, FirstName: "Sam"}, // a stranger from some group
	}}, 0)
	s.markDialog(1)
	s.markDialog(2)

	if got := s.byTitle("sam"); len(got) != 1 || got[0] != 1 {
		t.Errorf(`byTitle("sam") = %v, want [1]`, got)
	}
	if got := s.byTitle("jones"); len(got) != 1 || got[0] != 2 {
		t.Errorf(`byTitle("jones") = %v, want [2]`, got)
	}
	if got := s.byTitle("sa"); len(got) != 2 {
		t.Errorf(`byTitle("sa") = %v, want both chats`, got)
	}
}

func TestPeersPersist(t *testing.T) {
	s := testStore(t)
	s.learn(tg.Entities{Users: map[int64]*tg.User{7: {ID: 7, AccessHash: 70, FirstName: "Ann", Username: "ann"}}}, 0)
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	if id, ok := loadPeers(s.path).byUsername("ANN"); !ok || id != 7 {
		t.Fatalf("byUsername after reload = %d, %v", id, ok)
	}
}

// Private chats omit the sender: it is the account for outgoing messages and
// the other person for incoming ones.
func TestMessageAuthorInPrivateChat(t *testing.T) {
	b := &backend{self: core.User{ID: 1, Name: "Me"}, peers: testStore(t)}
	e := tg.Entities{Users: map[int64]*tg.User{7: {ID: 7, FirstName: "Ann"}}}

	in, _ := b.message(&tg.Message{ID: 5, PeerID: &tg.PeerUser{UserID: 7}, Message: "hi"}, e)
	if in.From != "Ann" || in.FromID != 7 || in.ChatID != 7 {
		t.Errorf("incoming = %+v", in)
	}
	out, _ := b.message(&tg.Message{ID: 6, Out: true, PeerID: &tg.PeerUser{UserID: 7}, Message: "yo",
		ReplyTo: &tg.MessageReplyHeader{ReplyToMsgID: 5}}, e)
	if out.From != "Me" || out.FromID != 1 || out.ReplyTo != 5 {
		t.Errorf("outgoing = %+v", out)
	}
	// Saved Messages: Telegram does not flag the account's own notes as outgoing.
	saved, _ := b.message(&tg.Message{ID: 7, PeerID: &tg.PeerUser{UserID: 1}, FromID: &tg.PeerUser{UserID: 1}}, e)
	if !saved.Out || saved.From != "Me" {
		t.Errorf("saved message = %+v", saved)
	}
	if _, ok := b.message(&tg.MessageEmpty{ID: 9}, e); ok {
		t.Error("empty messages must be skipped")
	}
}

func TestMediaSummary(t *testing.T) {
	voice := &tg.MessageMediaDocument{Document: &tg.Document{Attributes: []tg.DocumentAttributeClass{
		&tg.DocumentAttributeAudio{Voice: true, Duration: 72},
	}}}
	file := &tg.MessageMediaDocument{Document: &tg.Document{Attributes: []tg.DocumentAttributeClass{
		&tg.DocumentAttributeFilename{FileName: "report.pdf"},
	}}}
	// A video sticker carries video attributes too, often listed first.
	videoSticker := &tg.MessageMediaDocument{Document: &tg.Document{Attributes: []tg.DocumentAttributeClass{
		&tg.DocumentAttributeVideo{Duration: 3}, &tg.DocumentAttributeSticker{Alt: "😪"},
	}}}
	cases := map[string]tg.MessageMediaClass{
		"":                 nil,
		"photo":            &tg.MessageMediaPhoto{},
		"voice 1:12":       voice,
		"file: report.pdf": file,
		"sticker 😪":        videoSticker,
	}
	for want, media := range cases {
		if got, _ := mediaSummary(media); got != want {
			t.Errorf("mediaSummary(%T) = %q, want %q", media, got, want)
		}
	}
	// A spoiler is not drawn: that would give it away.
	for _, media := range []tg.MessageMediaClass{
		&tg.MessageMediaPhoto{Spoiler: true},
		&tg.MessageMediaDocument{Spoiler: true, Document: &tg.Document{Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeVideo{Duration: 3}}}},
	} {
		if _, kind := mediaSummary(media); kind != core.NoPicture {
			t.Errorf("spoiler %T is drawn", media)
		}
	}
}
