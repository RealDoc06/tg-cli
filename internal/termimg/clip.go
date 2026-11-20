package termimg

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/RealDoc06/tg-cli/internal/lottie"
)

// Clips are decoded at clipFPS into frames no larger than clipMaxSide
// pixels, and cut short past clipPixels pixels in all: the terminal keeps
// every frame uncompressed, and they cross the wire as PNG.
const (
	clipFPS     = 20
	clipMaxSide = 256
	clipPixels  = 4_000_000
)

// clipTimeout bounds the decoding of one clip.
const clipTimeout = 30 * time.Second

// decoders bounds how many clips are decoded at once.
var decoders = make(chan struct{}, 2)

// Magic numbers: WebM is the format of video stickers, gzip wraps the Lottie
// JSON of animated (TGS) stickers.
var (
	webm  = []byte{0x1A, 0x45, 0xDF, 0xA3}
	gzipM = []byte{0x1F, 0x8B}
)

// Frames turns a short muted clip into PNG frames for a cols x rows cell
// area, to transmit with Transmit and Play. Animated stickers are rendered
// here; videos (MP4, WebM) are decoded by ffmpeg, on the GPU when it can, and
// fail when ffmpeg is missing.
func Frames(ctx context.Context, clip []byte, cols, rows int) (frames [][]byte, gap time.Duration, err error) {
	if cols < 1 || rows < 1 {
		return nil, 0, errors.New("no room for the clip")
	}
	// The terminal stretches frames over the cells, so they take the cells'
	// proportions.
	w := min(cols*cellPx, clipMaxSide)
	h := max(1, w*rows*cellAspect/cols)
	if h > clipMaxSide {
		h = clipMaxSide
		w = max(1, h*cols/(rows*cellAspect))
	}

	select {
	case decoders <- struct{}{}:
		defer func() { <-decoders }()
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	}
	var images []image.Image
	if bytes.HasPrefix(clip, gzipM) {
		images, err = renderLottie(ctx, clip, w, h)
	} else {
		images, err = decode(ctx, clip, w, h)
	}
	if err != nil {
		return nil, 0, err
	}
	for _, img := range images {
		var out bytes.Buffer
		if err := png.Encode(&out, img); err != nil {
			return nil, 0, err
		}
		frames = append(frames, out.Bytes())
	}
	if len(frames) == 0 {
		return nil, 0, errors.New("clip has no frames")
	}
	return frames, time.Second / clipFPS, nil
}

// maxFrames is how many w x h frames fit in the budget of a clip.
func maxFrames(w, h int) int { return max(1, clipPixels/(w*h)) }

// renderLottie renders the frames of a TGS sticker. The file comes from
// anyone in the chat, so a panic of the renderer is an error like any other,
// and rendering stops at the deadline ffmpeg gets too.
func renderLottie(ctx context.Context, tgs []byte, w, h int) (images []image.Image, err error) {
	defer func() {
		if r := recover(); r != nil {
			images, err = nil, fmt.Errorf("animated sticker: %v", r)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, clipTimeout)
	defer cancel()
	zr, err := gzip.NewReader(bytes.NewReader(tgs))
	if err != nil {
		return nil, err
	}
	// Telegram caps stickers at 64 KB gzipped, this is far more than enough.
	data, err := io.ReadAll(io.LimitReader(zr, 4<<20))
	if err != nil {
		return nil, err
	}
	anim, err := lottie.Parse(data)
	if err != nil {
		return nil, err
	}
	step := anim.FrameRate / clipFPS
	n := min(maxFrames(w, h), max(1, int((anim.Out-anim.In)/step)))
	images = make([]image.Image, n)
	for i := range n {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if images[i], err = anim.Render(anim.In+float64(i)*step, w, h); err != nil {
			return nil, err
		}
	}
	return images, nil
}

// decode has ffmpeg turn a video into frames of w x h.
func decode(ctx context.Context, clip []byte, w, h int) ([]image.Image, error) {
	// MP4 files can keep their index at the end, out of reach of a pipe.
	file, err := os.CreateTemp("", "tg-clip-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(file.Name())
	_, err = file.Write(clip)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}

	// The file comes from anyone in the chat: ffmpeg reads it as the one
	// format it claims to be, never as a playlist that would fetch other
	// files or URLs, and refuses frames past 4K. Video stickers carry
	// transparency only libvpx decodes, in software. MP4 may use a hardware
	// decoder, ffmpeg falls back on its own.
	args := []string{"-v", "error", "-nostdin", "-protocol_whitelist", "file", "-max_pixels", "8294400"}
	if bytes.HasPrefix(clip, webm) {
		args = append(args, "-f", "matroska", "-c:v", "libvpx-vp9")
	} else {
		args = append(args, "-f", "mov", "-hwaccel", "auto")
	}
	args = append(args, "-i", file.Name(), "-an", "-map", "0:v:0",
		"-vf", fmt.Sprintf("fps=%d,scale=%d:%d", clipFPS, w, h),
		"-frames:v", fmt.Sprint(maxFrames(w, h)),
		"-f", "rawvideo", "-pix_fmt", "rgba", "pipe:1")

	ctx, cancel := context.WithTimeout(ctx, clipTimeout)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		// The last line: the first are often hardware decoders ffmpeg tried.
		lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
		return nil, fmt.Errorf("ffmpeg: %w: %s", err, lines[len(lines)-1])
	}
	var images []image.Image
	for size := w * h * 4; len(raw) >= size; raw = raw[size:] {
		// ffmpeg's rgba is not premultiplied, as NRGBA.
		images = append(images, &image.NRGBA{Pix: raw[:size], Stride: w * 4, Rect: image.Rect(0, 0, w, h)})
	}
	return images, nil
}

// Footprint is about how many bytes the terminal keeps for PNG frames: it
// stores them decoded.
func Footprint(frames [][]byte) int {
	if len(frames) == 0 {
		return 0
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(frames[0]))
	if err != nil {
		return 0
	}
	return cfg.Width * cfg.Height * 4 * len(frames)
}
