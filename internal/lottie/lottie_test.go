package lottie

import (
	"fmt"
	"image"
	"math"
	"strings"
	"testing"
	"time"
)

// The fixtures draw on a 100 x 100 canvas, rendered at its own size.

func render(t *testing.T, layers ...string) *image.RGBA {
	t.Helper()
	a, err := Parse([]byte(`{"fr":60,"ip":0,"op":60,"w":100,"h":100,"layers":[` + strings.Join(layers, ",") + `]}`))
	if err != nil {
		t.Fatal(err)
	}
	img, err := a.Render(0, 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// shapes is a shape layer with one group, props are extra layer keys. rc
// is a rectangle item: center and size.
func shapes(props string, items ...string) string {
	if props != "" {
		props += ","
	}
	return `{"ty":4,"ip":0,"op":60,"ks":{},` + props + `"shapes":[` + group(items...) + `]}`
}

func group(items ...string) string {
	return `{"ty":"gr","it":[` + strings.Join(append(items, `{"ty":"tr"}`), ",") + `]}`
}

func rc(x, y, w, h float64) string {
	return fmt.Sprintf(`{"ty":"rc","p":{"k":[%g,%g]},"s":{"k":[%g,%g]},"r":{"k":0}}`, x, y, w, h)
}

func fillOf(r, g, b float64) string {
	return fmt.Sprintf(`{"ty":"fl","c":{"k":[%g,%g,%g,1]},"o":{"k":100}}`, r, g, b)
}

const (
	red  = `{"ty":"fl","c":{"k":[1,0,0,1]},"o":{"k":100}}`
	blue = `{"ty":"st","c":{"k":[0,0,1,1]},"o":{"k":100},"w":{"k":10}}`
)

// pixel returns the premultiplied color at x, y.
func pixel(img *image.RGBA, x, y int) [4]uint8 {
	p := img.Pix[img.PixOffset(x, y):]
	return [4]uint8{p[0], p[1], p[2], p[3]}
}

func expect(t *testing.T, img *image.RGBA, x, y int, want [4]uint8) {
	t.Helper()
	if got := pixel(img, x, y); got != want {
		t.Errorf("pixel %d,%d = %v, want %v", x, y, got, want)
	}
}

var transparent [4]uint8

func opaque(r, g, b uint8) [4]uint8 { return [4]uint8{r, g, b, 255} }

// Items list the top one first: an earlier group covers a later one, and a
// fill or stroke paints the shapes above it, on top of the styles below.
func TestItemsPaintTopFirst(t *testing.T) {
	img := render(t, `{"ty":4,"ip":0,"op":60,"ks":{},"shapes":[`+
		group(rc(50, 50, 20, 20), fillOf(0, 1, 0))+","+
		group(rc(50, 50, 60, 60), blue, red)+`]}`)
	expect(t, img, 50, 50, opaque(0, 255, 0)) // the first group on top
	expect(t, img, 50, 21, opaque(0, 0, 255)) // the stroke over the fill
	expect(t, img, 50, 35, opaque(255, 0, 0))
	expect(t, img, 5, 5, transparent)
}

// Keyframes come in several forms: eased, held, and the older one where each
// key holds its end value and the last key only a time.
func TestKeyframes(t *testing.T) {
	cases := []struct {
		name  string
		k     string
		frame float64
		want  float64
	}{
		{"linear", `[{"t":0,"s":[0],"o":{"x":0,"y":0},"i":{"x":1,"y":1}},{"t":10,"s":[100]}]`, 5, 50},
		{"ease in out", `[{"t":0,"s":[0],"o":{"x":[0.42],"y":[0]},"i":{"x":[0.58],"y":[1]}},{"t":10,"s":[100]}]`, 5, 50},
		{"ease in out, early", `[{"t":0,"s":[0],"o":{"x":[0.42],"y":[0]},"i":{"x":[0.58],"y":[1]}},{"t":10,"s":[100]}]`, 2, 8.17},
		{"hold", `[{"t":0,"s":[0],"h":1},{"t":10,"s":[100]}]`, 9, 0},
		{"after the last key", `[{"t":0,"s":[0]},{"t":10,"s":[100]}]`, 20, 100},
		{"before the first key", `[{"t":5,"s":[30]},{"t":10,"s":[100]}]`, 0, 30},
		{"end values", `[{"t":0,"s":[0],"e":[40],"o":{"x":0,"y":0},"i":{"x":1,"y":1}},{"t":10}]`, 5, 20},
		{"bare last key", `[{"t":0,"s":[0],"e":[40]},{"t":10}]`, 15, 40},
	}
	for _, c := range cases {
		var p numProp
		if err := p.UnmarshalJSON([]byte(`{"a":1,"k":` + c.k + `}`)); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := p.scalar(c.frame, -1); !(math.Abs(got-c.want) <= 0.1) {
			t.Errorf("%s: %g at frame %g, want %g", c.name, got, c.frame, c.want)
		}
	}
}

// Trim paths keep a stretch of a path, shifted by the offset.
func TestTrimPathsKeepAStretch(t *testing.T) {
	line := `{"ty":"sh","ks":{"k":{"c":false,"v":[[10,50],[90,50]],"i":[[0,0],[0,0]],"o":[[0,0],[0,0]]}}}`
	for _, c := range []struct {
		offset      int
		left, right bool
	}{{0, true, false}, {180, false, true}} {
		trim := fmt.Sprintf(`{"ty":"tm","s":{"k":0},"e":{"k":50},"o":{"k":%d},"m":1}`, c.offset)
		img := render(t, shapes("", line, trim, blue))
		if got := pixel(img, 30, 50)[3] != 0; got != c.left {
			t.Errorf("offset %d: left half drawn = %v", c.offset, got)
		}
		if got := pixel(img, 70, 50)[3] != 0; got != c.right {
			t.Errorf("offset %d: right half drawn = %v", c.offset, got)
		}
	}
}

// A track matte shows its layer where the matte is (or is not, inverted). A
// mask cuts its own layer, and matte sources are not drawn themselves.
func TestMattesAndMasks(t *testing.T) {
	leftHalf := rc(25, 50, 50, 100)
	matte := shapes(`"td":1`, leftHalf, fillOf(1, 1, 1))
	full := rc(50, 50, 100, 100)

	img := render(t, matte, shapes(`"tt":1`, full, red))
	expect(t, img, 20, 50, opaque(255, 0, 0))
	expect(t, img, 80, 50, transparent)

	img = render(t, matte, shapes(`"tt":2`, full, red))
	expect(t, img, 20, 50, transparent)
	expect(t, img, 80, 50, opaque(255, 0, 0))

	mask := `"masksProperties":[{"mode":"s","inv":false,"o":{"k":100},"pt":{"k":{"c":true,"v":[[0,0],[50,0],[50,100],[0,100]],"i":[[0,0],[0,0],[0,0],[0,0]],"o":[[0,0],[0,0],[0,0],[0,0]]}}}]`
	img = render(t, shapes(mask, full, red))
	expect(t, img, 20, 50, transparent)
	expect(t, img, 80, 50, opaque(255, 0, 0))

	// A mask set to none does nothing at all.
	img = render(t, shapes(strings.Replace(mask, `"mode":"s"`, `"mode":"n"`, 1), full, red))
	expect(t, img, 20, 50, opaque(255, 0, 0))
}

// Stickers come from strangers: a file built to crash or stall the renderer
// must fail fast, or draw what it can.
func TestHostileFilesAreBounded(t *testing.T) {
	parse := func(data string) *Animation {
		a, err := Parse([]byte(data))
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	precomp := func(ref string) string {
		return `{"ty":0,"refId":"` + ref + `","ip":0,"op":60,"ks":{}}`
	}

	// Each precomp draws the next four times: 4^15 layers.
	var assets []string
	for i := range 15 {
		next := fmt.Sprintf("a%d", i+1)
		assets = append(assets, fmt.Sprintf(`{"id":"a%d","layers":[%s,%s,%s,%s]}`, i, precomp(next), precomp(next), precomp(next), precomp(next)))
	}
	assets = append(assets, `{"id":"a15","layers":[`+shapes("", rc(50, 50, 10, 10), red)+`]}`)
	fanOut := parse(`{"fr":60,"ip":0,"op":60,"w":100,"h":100,"layers":[` + precomp("a0") + `],"assets":[` + strings.Join(assets, ",") + `]}`)
	if _, err := fanOut.Render(0, 100, 100); err != ErrTooComplex {
		t.Errorf("fan out: %v", err)
	}

	// Hundreds of trims over one long curve, each walking all its points.
	var v, tangents []string
	for i := range 1000 {
		v = append(v, fmt.Sprintf("[%d,%d]", i%100, i%37))
		tangents = append(tangents, "[30,-30]")
	}
	curve := `{"ty":"sh","ks":{"k":{"c":true,"v":[` + strings.Join(v, ",") + `],"i":[` + strings.Join(tangents, ",") + `],"o":[` + strings.Join(tangents, ",") + `]}}}`
	items := []string{curve}
	for range 900 {
		items = append(items, `{"ty":"tm","s":{"k":0.1},"e":{"k":100},"o":{"k":0},"m":1}`)
	}
	trims := parse(`{"fr":60,"ip":0,"op":60,"w":100,"h":100,"layers":[` + shapes("", append(items, blue)...) + `]}`)
	start := time.Now()
	if _, err := trims.Render(0, 256, 256); err != ErrTooComplex || time.Since(start) > 2*time.Second {
		t.Errorf("trims: %v after %v", err, time.Since(start))
	}

	cases := map[string]string{
		"a precomp drawing itself": `{"fr":60,"ip":0,"op":60,"w":100,"h":100,"layers":[` + precomp("a") + `],"assets":[{"id":"a","layers":[` + precomp("a") + `,` + shapes("", rc(50, 50, 10, 10), red) + `]}]}`,
		"negative gradient stops":  `{"fr":60,"ip":0,"op":60,"w":100,"h":100,"layers":[` + shapes("", rc(50, 50, 10, 10), `{"ty":"gf","t":1,"s":{"k":[0,0]},"e":{"k":[10,0]},"o":{"k":100},"g":{"p":-1,"k":{"k":[0,1,0,0,1,0,0,1]}}}`) + `]}`,
		"a shape far off canvas":   `{"fr":60,"ip":0,"op":60,"w":100,"h":100,"layers":[` + shapes("", rc(50, 50, 1e9, 1e9), red) + `]}`,
	}
	for name, data := range cases {
		if _, err := parse(data).Render(0, 100, 100); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A layer's keyframes are in its composition's time, whatever its start
// time: that only shifts the time inside a precomp.
func TestStartTimeShiftsOnlyPrecomps(t *testing.T) {
	fade := `"st":20,"ks":{"o":{"a":1,"k":[{"t":30,"s":[0],"o":{"x":0,"y":0},"i":{"x":1,"y":1}},{"t":50,"s":[100]}]}}`
	layer := strings.Replace(shapes("", rc(50, 50, 100, 100), red), `"ks":{},`, fade+",", 1)
	a, err := Parse([]byte(`{"fr":60,"ip":0,"op":60,"w":100,"h":100,"layers":[` + layer + `]}`))
	if err != nil {
		t.Fatal(err)
	}
	img, err := a.Render(40, 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	if alpha := pixel(img, 50, 50)[3]; alpha < 120 || alpha > 135 {
		t.Errorf("opacity %d/255 at frame 40, want half", alpha)
	}

	// Inside a precomp starting at 20, the same layer without a start time
	// is at its frame 20 when the composition is at 40.
	inner := strings.Replace(layer, `"st":20,`, ``, 1)
	inner = strings.Replace(inner, `"t":30`, `"t":10`, 1)
	inner = strings.Replace(inner, `"t":50`, `"t":30`, 1)
	a, err = Parse([]byte(`{"fr":60,"ip":0,"op":60,"w":100,"h":100,"layers":[{"ty":0,"refId":"p","st":20,"ip":0,"op":60,"ks":{}}],"assets":[{"id":"p","layers":[` + inner + `]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if img, err = a.Render(40, 100, 100); err != nil {
		t.Fatal(err)
	}
	if alpha := pixel(img, 50, 50)[3]; alpha < 120 || alpha > 135 {
		t.Errorf("precomp: opacity %d/255 at frame 40, want half", alpha)
	}
}

// A child layer moves with its parent, a null layer that draws nothing.
func TestChildFollowsParent(t *testing.T) {
	parent := `{"ty":3,"ind":1,"ip":0,"op":60,"ks":{"p":{"k":[70,50]},"r":{"k":90}}}`
	child := `{"ty":4,"ind":2,"parent":1,"ip":0,"op":60,"ks":{},"shapes":[` + group(rc(10, 0, 10, 6), red) + `]}`
	img := render(t, child, parent)
	expect(t, img, 70, 60, opaque(255, 0, 0)) // rotated a quarter, clockwise
	expect(t, img, 80, 50, transparent)
	expect(t, img, 10, 10, transparent)
}
