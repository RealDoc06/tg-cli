// Package termimg draws images in terminals that implement the Kitty graphics
// protocol with Unicode placeholders (Kitty, Ghostty). An image is transmitted
// once with Transmit, then shown anywhere by printing the text returned by
// Placeholder, which makes it safe to use inside a cell based TUI renderer.
package termimg

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg" // profile photos are JPEG
	"image/png"
	"os"
	"strings"

	"golang.org/x/image/draw"
)

// Supported reports whether the terminal can show placeholder images. It is
// decided from the environment because the protocol's capability query cannot
// tell whether Unicode placeholders are implemented. TG_IMAGES=0 or 1
// overrides the detection.
func Supported() bool {
	switch os.Getenv("TG_IMAGES") {
	case "0":
		return false
	case "1":
		return true
	}
	return os.Getenv("KITTY_WINDOW_ID") != "" ||
		strings.Contains(os.Getenv("TERM"), "kitty") ||
		strings.Contains(os.Getenv("TERM"), "ghostty") ||
		os.Getenv("TERM_PROGRAM") == "ghostty" ||
		os.Getenv("GHOSTTY_RESOURCES_DIR") != ""
}

// MaxID is the highest usable image ID. IDs travel in the 256 color
// foreground of the placeholder cells, which survives any color profile.
const MaxID = 255

const placeholder = '\U0010EEEE'

// diacritics encode row and column numbers of placeholder cells, as listed in
// the Kitty graphics protocol. Only the first entries are needed for avatars.
var diacritics = []rune{
	0x0305, 0x030D, 0x030E, 0x0310, 0x0312, 0x033D, 0x033E, 0x033F,
	0x0346, 0x034A, 0x034B, 0x034C, 0x0350, 0x0351, 0x0352, 0x0357,
}

// Placeholder returns the text lines that display image id over a cols x rows
// cell area. Every cell is fully addressed so partial redraws stay correct.
func Placeholder(id, cols, rows int) []string {
	lines := make([]string, rows)
	for r := range rows {
		var b strings.Builder
		fmt.Fprintf(&b, "\x1b[38;5;%dm", id)
		for c := range cols {
			b.WriteRune(placeholder)
			b.WriteRune(diacritics[r])
			b.WriteRune(diacritics[c])
		}
		b.WriteString("\x1b[39m")
		lines[r] = b.String()
	}
	return lines
}

// Transmit returns the escape sequence that uploads PNG data as image id and
// creates the virtual placement the placeholder cells refer to.
func Transmit(id, cols, rows int, pngData []byte) string {
	const chunk = 4096
	enc := base64.StdEncoding.EncodeToString(pngData)
	var b strings.Builder
	for first := true; first || enc != ""; first = false {
		n := min(chunk, len(enc))
		more := 0
		if n < len(enc) {
			more = 1
		}
		var seq string
		if first {
			seq = fmt.Sprintf("\x1b_Gf=100,a=T,U=1,q=2,i=%d,c=%d,r=%d,m=%d;%s\x1b\\", id, cols, rows, more, enc[:n])
		} else {
			seq = fmt.Sprintf("\x1b_Gm=%d;%s\x1b\\", more, enc[:n])
		}
		b.WriteString(passthrough(seq))
		enc = enc[n:]
	}
	return b.String()
}

// DeleteAll returns the sequence that frees every image this process uploaded.
func DeleteAll(ids []int) string {
	var b strings.Builder
	for _, id := range ids {
		b.WriteString(passthrough(fmt.Sprintf("\x1b_Ga=d,d=I,q=2,i=%d\x1b\\", id)))
	}
	return b.String()
}

// passthrough wraps a sequence so tmux forwards it to the outer terminal.
func passthrough(seq string) string {
	if os.Getenv("TMUX") == "" {
		return seq
	}
	return "\x1bPtmux;" + strings.ReplaceAll(seq, "\x1b", "\x1b\x1b") + "\x1b\\"
}

// Avatar decodes an image, scales it to a size x size square and cuts it into
// a circle, returning PNG data ready for Transmit.
func Avatar(data []byte, size int) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, centerSquare(src.Bounds()), draw.Src, nil)

	// Anti aliased circular mask.
	radius := float64(size) / 2
	for y := range size {
		for x := range size {
			dx, dy := float64(x)+0.5-radius, float64(y)+0.5-radius
			edge := radius*radius - (dx*dx + dy*dy)
			switch {
			case edge <= 0:
				dst.SetNRGBA(x, y, color.NRGBA{})
			case edge < radius*2:
				px := dst.NRGBAAt(x, y)
				px.A = uint8(float64(px.A) * edge / (radius * 2))
				dst.SetNRGBA(x, y, px)
			}
		}
	}

	var out bytes.Buffer
	if err := png.Encode(&out, dst); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// centerSquare returns the largest centered square inside r.
func centerSquare(r image.Rectangle) image.Rectangle {
	side := min(r.Dx(), r.Dy())
	x := r.Min.X + (r.Dx()-side)/2
	y := r.Min.Y + (r.Dy()-side)/2
	return image.Rect(x, y, x+side, y+side)
}
