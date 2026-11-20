package clipboard

import (
	"bytes"
	"testing"
)

// The bytes come from the paste events specification, so tg speaks the same
// protocol as the terminal.
func TestPasteEventPackets(t *testing.T) {
	ok, okParsed := ParsePacket("\x1b]5522;type=read:status=OK:pw=c2VjcmV0MTIz\x1b\\")
	list, listParsed := ParsePacket("\x1b]5522;type=read:status=DATA:mime=Lg==:pw=c2VjcmV0MTIz;dGV4dC9wbGFpbgo=\x1b\\")
	if !okParsed || !listParsed || ok.Status != "OK" || ok.PW != "secret123" {
		t.Fatalf("OK packet: %+v", ok)
	}
	if list.Status != "DATA" || list.MIME != ListTypes || string(list.Data) != "text/plain\n" {
		t.Errorf("type list: %+v", list)
	}
	want := "\x1b]5522;type=read:id=7:pw=c2VjcmV0MTIz:name=UGFzdGUgZXZlbnQ=;dGV4dC9wbGFpbg==\x1b\\"
	if got := ReadRequest(7, "secret123", "Paste event", "text/plain"); got != want {
		t.Errorf("request %q, want %q", got, want)
	}
	if p, _ := ParsePacket("\x1b]5522;type=read:status=DONE:id=7\x1b\\"); p.ID != 7 {
		t.Errorf("reply id %d, want 7", p.ID)
	}
	if _, ok := ParsePacket("\x1b]52;c;aGk=\x1b\\"); ok {
		t.Error("an OSC 52 reply was taken for OSC 5522")
	}
}

func TestPickPrefersImagesOnlyWhenWanted(t *testing.T) {
	list := []byte("text/html image/png text/plain;charset=utf-8\n")
	if got := Pick(list, true); got != "image/png" {
		t.Errorf("composer picked %q", got)
	}
	if got := Pick(list, false); got != "text/plain;charset=utf-8" {
		t.Errorf("chat filter picked %q", got)
	}
	if got := Pick([]byte("text/html"), true); got != "" {
		t.Errorf("picked %q from a clipboard without image or text", got)
	}
	if !bytes.Equal([]byte(Text(map[string][]byte{"text/plain": []byte("hi")})), []byte("hi")) {
		t.Error("text/plain data is not text")
	}
}
