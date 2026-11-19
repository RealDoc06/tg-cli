package mtp

import (
	"fmt"
	"strings"
	"sync"

	"github.com/gotd/td/tg"

	"github.com/RealDoc06/tg-cli/internal/config"
	"github.com/RealDoc06/tg-cli/internal/core"
)

// channelBase offsets channel IDs into the Bot API style ID space.
const channelBase = 1_000_000_000_000

func userChatID(id int64) int64    { return id }
func groupChatID(id int64) int64   { return -id }
func channelChatID(id int64) int64 { return -channelBase - id }

// chatIDOf converts a Telegram peer into a core.Chat ID.
func chatIDOf(p tg.PeerClass) int64 {
	switch p := p.(type) {
	case *tg.PeerUser:
		return userChatID(p.UserID)
	case *tg.PeerChat:
		return groupChatID(p.ChatID)
	case *tg.PeerChannel:
		return channelChatID(p.ChannelID)
	}
	return 0
}

// peer is what is remembered about a user, group or channel: enough to
// address it again (the access hash) and to show it.
type peer struct {
	Kind     core.ChatKind `json:"k"`
	Hash     int64         `json:"h,omitempty"`
	Title    string        `json:"t"`
	Username string        `json:"u,omitempty"`
	Photo    int64         `json:"p,omitempty"`
	// Dialog marks peers seen in the chat list. Only those can be addressed
	// by title, so a message never goes to a stranger who shares a name.
	Dialog bool `json:"d,omitempty"`
}

// peerStore caches peers on disk. Telegram only lets a client address peers
// whose access hash it has seen, and remembering them saves a chat list fetch
// on every command.
type peerStore struct {
	mu    sync.Mutex
	path  string
	peers map[int64]peer
	dirty bool
}

// loadPeers reads the cache of one account. Access hashes only work for the
// account that received them, so accounts never share a cache.
func loadPeers(path string) *peerStore {
	s := &peerStore{path: path, peers: map[int64]peer{}}
	_ = config.ReadJSON(path, &s.peers) // a missing or broken cache is rebuilt
	return s
}

func (s *peerStore) save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty {
		return nil
	}
	s.dirty = false
	return config.WriteJSON(s.path, s.peers)
}

func (s *peerStore) get(id int64) (peer, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.peers[id]
	return p, ok
}

// put records p, keeping what a later, less complete sighting must not erase.
func (s *peerStore) put(id int64, p peer, min bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, known := s.peers[id]
	if known && min {
		p.Hash = old.Hash // "min" constructors carry no usable access hash
	}
	p.Dialog = p.Dialog || old.Dialog
	if !known || old != p {
		s.peers[id] = p
		s.dirty = true
	}
}

func (s *peerStore) markDialog(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.peers[id]; ok && !p.Dialog {
		p.Dialog = true
		s.peers[id] = p
		s.dirty = true
	}
}

// learn stores every user, group and channel of an API response.
func (s *peerStore) learn(e tg.Entities, selfID int64) {
	for id, u := range e.Users {
		p := peer{Kind: core.KindUser, Hash: u.AccessHash, Title: userName(u), Username: u.Username}
		if u.Bot {
			p.Kind = core.KindBot
		}
		if id == selfID {
			p.Title = "Saved Messages"
		}
		if photo, ok := u.Photo.(*tg.UserProfilePhoto); ok {
			p.Photo = photo.PhotoID
		}
		s.put(userChatID(id), p, u.Min)
	}
	for id, c := range e.Chats {
		p := peer{Kind: core.KindGroup, Title: c.Title}
		if photo, ok := c.Photo.(*tg.ChatPhoto); ok {
			p.Photo = photo.PhotoID
		}
		s.put(groupChatID(id), p, false)
	}
	for id, c := range e.Channels {
		p := peer{Kind: core.KindGroup, Hash: c.AccessHash, Title: c.Title, Username: c.Username}
		if c.Broadcast {
			p.Kind = core.KindChannel
		}
		if photo, ok := c.Photo.(*tg.ChatPhoto); ok {
			p.Photo = photo.PhotoID
		}
		s.put(channelChatID(id), p, c.Min)
	}
}

// chat builds the core.Chat of a known peer.
func (s *peerStore) chat(id int64) (core.Chat, bool) {
	p, ok := s.get(id)
	return core.Chat{ID: id, Kind: p.Kind, Title: p.Title, Username: p.Username, PhotoID: p.Photo}, ok
}

// input returns the addressable form of a chat ID.
func (s *peerStore) input(id int64) (tg.InputPeerClass, error) {
	p, ok := s.get(id)
	if !ok {
		return nil, fmt.Errorf("unknown chat %d, list it with `tg chats` or address it by @username", id)
	}
	switch {
	case id > 0:
		return &tg.InputPeerUser{UserID: id, AccessHash: p.Hash}, nil
	case id < -channelBase:
		return &tg.InputPeerChannel{ChannelID: -id - channelBase, AccessHash: p.Hash}, nil
	default:
		return &tg.InputPeerChat{ChatID: -id}, nil
	}
}

// byUsername finds a cached peer by its public username.
func (s *peerStore) byUsername(username string) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, p := range s.peers {
		if p.Username != "" && strings.EqualFold(p.Username, username) {
			return id, true
		}
	}
	return 0, false
}

// byTitle returns the chat list entries matching a title: exact matches if
// there are any, otherwise substring matches.
func (s *peerStore) byTitle(title string) []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	needle := strings.ToLower(title)
	var exact, partial []int64
	for id, p := range s.peers {
		if !p.Dialog {
			continue
		}
		switch t := strings.ToLower(p.Title); {
		case t == needle:
			exact = append(exact, id)
		case strings.Contains(t, needle):
			partial = append(partial, id)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return partial
}

func userName(u *tg.User) string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	switch {
	case name != "":
		return name
	case u.Deleted:
		return "Deleted Account"
	case u.Username != "":
		return u.Username
	}
	return fmt.Sprintf("User %d", u.ID)
}
