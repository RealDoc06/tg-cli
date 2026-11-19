package termimg

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// The TUI lays out avatars by cell count, so a placeholder line must measure
// exactly its column count even though every cell carries two diacritics.
func TestPlaceholderOccupiesItsCells(t *testing.T) {
	lines := Placeholder(42, 4, 2)
	if len(lines) != 2 {
		t.Fatalf("got %d lines", len(lines))
	}
	for _, line := range lines {
		if w := ansi.StringWidth(line); w != 4 {
			t.Errorf("line is %d cells wide, want 4", w)
		}
		if !strings.HasPrefix(line, "\x1b[38;5;42m") {
			t.Errorf("image ID is not in the foreground color: %q", line)
		}
	}
	if lines[0] == lines[1] {
		t.Error("rows are not distinguished")
	}
}

func TestTransmitChunksLargeImages(t *testing.T) {
	data := bytes.Repeat([]byte{0xAB, 0x01, 0xFE}, 5000)
	seq := Transmit(7, 4, 2, data)

	chunks := regexp.MustCompile(`\x1b_G([^;]*);([^\x1b]*)\x1b\\`).FindAllStringSubmatch(seq, -1)
	if len(chunks) < 2 {
		t.Fatalf("got %d chunks for %d bytes", len(chunks), len(data))
	}
	var payload strings.Builder
	for i, c := range chunks {
		last := i == len(chunks)-1
		if strings.Contains(c[1], "m=1") == last {
			t.Errorf("chunk %d has the wrong continuation flag: %q", i, c[1])
		}
		if len(c[2]) > 4096 {
			t.Errorf("chunk %d is %d bytes", i, len(c[2]))
		}
		payload.WriteString(c[2])
	}
	if !strings.Contains(chunks[0][1], "i=7") || !strings.Contains(chunks[0][1], "U=1") {
		t.Errorf("first chunk lacks the image ID or placeholder flag: %q", chunks[0][1])
	}
	got, err := base64.StdEncoding.DecodeString(payload.String())
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("payload does not round trip: %v", err)
	}
}

func TestAvatarIsRoundAndSquare(t *testing.T) {
	var src bytes.Buffer
	wide := image.NewNRGBA(image.Rect(0, 0, 300, 100))
	for i := range wide.Pix {
		wide.Pix[i] = 0xFF
	}
	if err := png.Encode(&src, wide); err != nil {
		t.Fatal(err)
	}

	out, err := Avatar(src.Bytes(), 64)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 64 || b.Dy() != 64 {
		t.Fatalf("avatar is %v", b)
	}
	if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
		t.Error("corner is not transparent")
	}
	if _, _, _, a := img.At(32, 32).RGBA(); a != 0xFFFF {
		t.Error("center is not opaque")
	}
}
