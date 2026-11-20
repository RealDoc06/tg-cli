package termimg

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
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
	for _, line := range Placeholder(1, MaxCells, MaxCells) {
		if w := ansi.StringWidth(line); w != MaxCells {
			t.Fatalf("a %d cell wide line is %d cells", MaxCells, w)
		}
	}
}

// Fit must keep a picture's proportions inside the box it is given: a wide
// one uses every column, a tall one every row.
func TestFitKeepsProportions(t *testing.T) {
	encode := func(w, h int) []byte {
		var buf bytes.Buffer
		if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, w, h))); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	cases := []struct {
		w, h, cols, rows int
	}{
		{800, 400, 36, 9},   // 2:1, cells are twice as tall as wide
		{400, 800, 7, 14},   // tall, bound by rows
		{100, 100, 28, 14},  // square
		{4000, 10, 36, 1},   // never less than one row
		{900, 300, 300, 64}, // never more cells than placeholders can address
	}
	for _, c := range cases {
		_, cols, rows, err := Fit(encode(c.w, c.h), c.cols, c.rows)
		if err != nil {
			t.Fatal(err)
		}
		ratio := float64(c.w) / float64(c.h) * cellAspect // columns per row
		if cols > min(c.cols, MaxCells) || rows > min(c.rows, MaxCells) || (cols != min(c.cols, MaxCells) && rows != min(c.rows, MaxCells)) {
			t.Errorf("%dx%d in %dx%d: got %dx%d cells", c.w, c.h, c.cols, c.rows, cols, rows)
		}
		if got := float64(cols) / float64(rows); rows > 1 && (got < ratio*0.85 || got > ratio*1.15) {
			t.Errorf("%dx%d: %dx%d cells distort it", c.w, c.h, cols, rows)
		}
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

// The initials must sit in the middle of the badge, which text in cells
// cannot do.
func TestInitialsAreCentered(t *testing.T) {
	const size = 96
	bg, fg := color.NRGBA{200, 50, 50, 255}, color.NRGBA{255, 255, 255, 255}
	data, err := Initials("SM", bg, fg, size)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != size || img.Bounds().Dy() != size {
		t.Fatalf("badge is %v, want %dx%d", img.Bounds(), size, size)
	}
	ink := image.Rectangle{Min: image.Pt(size, size)}
	for y := range size {
		for x := range size {
			if r, _, _, _ := img.At(x, y).RGBA(); r>>8 != 200 { // not the background
				ink = ink.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	if ink.Empty() {
		t.Fatal("no text was drawn")
	}
	cx, cy := (ink.Min.X+ink.Max.X)/2, (ink.Min.Y+ink.Max.Y)/2
	if abs(cx-size/2) > 2 || abs(cy-size/2) > 2 {
		t.Errorf("text centered at (%d, %d), want (%d, %d)", cx, cy, size/2, size/2)
	}
}

func abs(n int) int { return max(n, -n) }
