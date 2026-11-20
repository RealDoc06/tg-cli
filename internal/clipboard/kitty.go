package clipboard

import (
	"encoding/base64"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Kitty's clipboard protocol, OSC 5522, reads the clipboard through the
// terminal, so it reaches the user's own clipboard even over SSH. With paste
// events on, the terminal reports every paste as the list of types on the
// clipboard, with a one-time password that reads them without a prompt.
// See https://sw.kovidgoyal.net/kitty/clipboard/

const (
	// PasteEventsOn turns paste events on and asks whether the terminal
	// knows them. The answer is a report of PasteEventsMode.
	PasteEventsOn = "\x1b[?5522h\x1b[?5522$p"
	// PasteEventsOff gives the terminal its usual pastes back.
	PasteEventsOff  = "\x1b[?5522l"
	PasteEventsMode = ansi.DECMode(5522)
)

// ListTypes is the type to read for the list of types on the clipboard.
const ListTypes = "."

// textTypes are the names text goes by, in order of preference.
var textTypes = []string{"text/plain;charset=utf-8", "text/plain", "UTF8_STRING"}

// Packet is one OSC 5522 reply to a read. A reply is an OK packet, DATA
// packets, then a DONE packet, or a single packet with an error status like
// EPERM.
type Packet struct {
	Status string
	ID     int    // of the request it answers, 0 on paste events
	MIME   string // of Data
	PW     string // one-time password, on paste events
	Data   []byte
}

// ParsePacket decodes an OSC 5522 read reply. It reports false for any
// other sequence.
func ParsePacket(seq string) (Packet, bool) {
	body, ok := strings.CutPrefix(seq, "\x1b]5522;")
	if !ok {
		return Packet{}, false
	}
	body = strings.TrimSuffix(strings.TrimSuffix(body, "\x1b\\"), "\a")
	meta, payload, _ := strings.Cut(body, ";")
	var p Packet
	var read bool
	for _, kv := range strings.Split(meta, ":") {
		key, value, _ := strings.Cut(kv, "=")
		switch key {
		case "type":
			read = value == "read"
		case "status":
			p.Status = value
		case "id":
			p.ID, _ = strconv.Atoi(value)
		case "mime":
			p.MIME = decode(value)
		case "pw":
			p.PW = decode(value)
		}
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if !read || p.Status == "" || err != nil {
		return Packet{}, false
	}
	p.Data = data
	return p, true
}

// ReadRequest asks the terminal for the clipboard data of one type, or for
// the list of types with ListTypes. The terminal tags every packet of its
// reply with id. pw and name identify the request: the one-time password of
// a paste event with "Paste event", or a password of this run that the user
// can allow once for all requests.
func ReadRequest(id int, pw, name, mime string) string {
	return "\x1b]5522;type=read:id=" + strconv.Itoa(id) + ":pw=" + encode(pw) + ":name=" + encode(name) + ";" + encode(mime) + "\x1b\\"
}

// Pick chooses what to read from a list of types: a PNG image when images
// are wanted and there is one, otherwise text. It returns "" when the
// clipboard has neither.
func Pick(list []byte, images bool) string {
	types := strings.Fields(string(list))
	if images && slices.Contains(types, "image/png") {
		return "image/png"
	}
	for _, t := range textTypes {
		if slices.Contains(types, t) {
			return t
		}
	}
	return ""
}

// Text returns the text among read data, "" when there is none.
func Text(data map[string][]byte) string {
	for _, t := range textTypes {
		if d, ok := data[t]; ok {
			return string(d)
		}
	}
	return ""
}

func encode(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func decode(s string) string {
	b, _ := base64.StdEncoding.DecodeString(s)
	return string(b)
}
