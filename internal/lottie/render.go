package lottie

import (
	"errors"
	"image"
	"image/color"
	"image/draw"
	"math"
	"slices"
	"strconv"
	"strings"
)

// ErrTooComplex is returned for a frame that takes more work to draw than
// any real sticker does, such as precomps that use each other over and over.
var ErrTooComplex = errors.New("lottie: animation is too complex to draw")

// workBudget bounds the work of one frame, counted in polygon points,
// layers, and pixels painted (pixelWork to a unit). The stickers of
// Telegram's own apps stay under a tenth of it.
const workBudget = 1_000_000

// Render draws the animation at a frame, scaled to w x h pixels.
func (a *Animation) Render(frame float64, w, h int) (*image.RGBA, error) {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	r := renderer{anim: a, raster: newRaster(dst.Rect), canvas: dst.Rect}
	r.layers(dst, a.Layers, frame, scale(float64(w)/a.Width, float64(h)/a.Height), 0)
	if r.raster.work > workBudget {
		return nil, ErrTooComplex
	}
	return dst, nil
}

type renderer struct {
	anim   *Animation
	raster *raster
	canvas image.Rectangle
	spare  []*image.RGBA // cleared layer buffers to reuse
	inside []string      // the precomps being drawn, to skip cycles
}

// maxDepth bounds precomps nested in precomps, and parent chains, which a
// broken file could make endless.
const maxDepth = 16

func (r *renderer) buffer() *image.RGBA {
	if n := len(r.spare); n > 0 {
		buf := r.spare[n-1]
		r.spare = r.spare[:n-1]
		return buf
	}
	return image.NewRGBA(r.canvas)
}

func (r *renderer) release(buf *image.RGBA) {
	clear(buf.Pix)
	r.spare = append(r.spare, buf)
}

// layers draws a composition's layers onto dst, bottom one first. frame is
// in the composition's time, base maps it to pixels.
func (r *renderer) layers(dst *image.RGBA, ls []layer, frame float64, base affine, depth int) {
	// Even layers that draw nothing are looked through, once per use.
	if r.raster.work += len(ls); depth > maxDepth || r.raster.work > workBudget {
		return
	}
	byIndex := map[int]*layer{}
	for i := range ls {
		if ls[i].Index != nil {
			byIndex[*ls[i].Index] = &ls[i]
		}
	}
	for i := len(ls) - 1; i >= 0 && r.raster.work <= workBudget; i-- {
		l := &ls[i]
		if l.MatteSource != 0 || !l.visible(frame) {
			continue
		}
		if l.MatteMode == 0 {
			r.layer(dst, l, frame, base, byIndex, depth)
			continue
		}
		// The layer and its matte, each alone.
		if r.raster.work += 2 * r.canvas.Dx() * r.canvas.Dy() / pixelWork; r.raster.work > workBudget {
			return
		}
		buf := r.buffer()
		r.layer(buf, l, frame, base, byIndex, depth)
		src := (*layer)(nil)
		if l.MatteParent != nil {
			src = byIndex[*l.MatteParent]
		} else if i > 0 {
			src = &ls[i-1]
		}
		matte := r.buffer()
		if src != nil && frame >= src.In && frame < src.Out {
			r.layer(matte, src, frame, base, byIndex, depth)
		}
		applyMatte(buf, matte, l.MatteMode)
		draw.Draw(dst, dst.Rect, buf, image.Point{}, draw.Over)
		r.release(buf)
		r.release(matte)
	}
}

// layer draws one layer onto dst. A layer with masks or partly transparent
// is drawn on its own first, as a whole.
func (r *renderer) layer(dst *image.RGBA, l *layer, frame float64, base affine, byIndex map[int]*layer, depth int) {
	if r.raster.work += layerWork; r.raster.work > workBudget {
		return
	}
	// A layer's own keyframes are in its composition's time. Its start time
	// only shifts the time of what a precomp holds, as lottie-web does.
	m := base.mul(world(l, frame, byIndex, 0))
	opacity := l.Ks.Opacity.scalar(frame, 100) / 100
	if opacity <= 0 || l.Type == layerNull {
		return
	}
	alone := len(l.Masks) > 0 || opacity < 1
	out := dst
	if alone {
		// A buffer of its own, its masks and compositing touch the canvas.
		if r.raster.work += (1 + len(l.Masks)) * r.canvas.Dx() * r.canvas.Dy() / pixelWork; r.raster.work > workBudget {
			return
		}
		out = r.buffer()
		defer r.release(out)
	}
	switch l.Type {
	case layerShape:
		b := builder{frame: frame, work: &r.raster.work}
		b.group(l.Shapes, m, 1)
		b.draw(r.raster, out)
	case layerSolid:
		c, ok := hexColor(l.SolidColor)
		if !ok {
			return
		}
		rect := bezier{Closed: true, V: []vec{{0, 0}, {l.SolidWidth, 0}, {l.SolidWidth, l.SolidHeight}, {0, l.SolidHeight}}, In: make([]vec, 4), Out: make([]vec, 4)}
		fill(r.raster, out, []polyline{flatten(rect, m)}, solidOf(c))
	case layerPrecomp:
		stretch := l.Stretch
		if stretch == 0 {
			stretch = 1
		}
		if slices.Contains(r.inside, l.RefID) {
			return
		}
		r.inside = append(r.inside, l.RefID)
		r.layers(out, r.anim.precomps[l.RefID], (frame-l.Start)/stretch, m, depth+1)
		r.inside = r.inside[:len(r.inside)-1]
	default:
		return
	}
	if !alone {
		return
	}
	if len(l.Masks) > 0 {
		r.applyMasks(out, l.Masks, frame, m)
	}
	scaleAlpha(out, opacity)
	draw.Draw(dst, dst.Rect, out, image.Point{}, draw.Over)
}

// world is a layer's transform in its composition, through its parents. In
// After Effects, parents pass on their transform but not their opacity.
func world(l *layer, frame float64, byIndex map[int]*layer, depth int) affine {
	m := l.Ks.matrix(frame)
	if l.Parent != nil && depth < maxDepth {
		if p := byIndex[*l.Parent]; p != nil && p != l {
			m = world(p, frame, byIndex, depth+1).mul(m)
		}
	}
	return m
}

// matrix is translate(p) · rotate(r) · skew · scale(s) · translate(-a).
func (t *transform) matrix(frame float64) affine {
	a := t.Anchor.at(frame, 0, 0)
	p := t.Position.at(frame, 0, 0)
	s := t.Scale.at(frame, 100, 100)
	if len(a) < 2 || len(p) < 2 || len(s) < 2 {
		return identity
	}
	m := translate(p[0], p[1]).mul(rotate(t.Rotation.scalar(frame, 0)))
	if sk := t.Skew.scalar(frame, 0); sk != 0 {
		axis := t.SkewAxis.scalar(frame, 0)
		m = m.mul(rotate(-axis)).mul(affine{a: 1, d: 1, c: math.Tan(-sk * math.Pi / 180)}).mul(rotate(axis))
	}
	return m.mul(scale(s[0]/100, s[1]/100)).mul(translate(-a[0], -a[1]))
}

// applyMasks keeps the parts of a layer its masks select. Mask paths are in
// layer space.
func (r *renderer) applyMasks(buf *image.RGBA, masks []mask, frame float64, m affine) {
	masks = slices.DeleteFunc(slices.Clone(masks), func(mk mask) bool { return mk.Mode == "n" })
	if len(masks) == 0 {
		return // masks that do nothing leave the layer whole
	}
	b := buf.Rect
	cover := make([]float64, b.Dx()*b.Dy())
	if masks[0].Mode == "s" || masks[0].Mode == "i" || masks[0].Mode == "d" {
		for i := range cover {
			cover[i] = 1 // subtracting and intersecting start from everything
		}
	}
	for _, mk := range masks {
		if r.raster.work > workBudget {
			return
		}
		r.raster.polygon(flatten(mk.Path.at(frame), m).pts...)
		alpha := r.raster.coverage(b)
		o := mk.Opacity.scalar(frame, 100) / 100
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				var v float64
				if alpha != nil && (image.Point{x, y}).In(alpha.Rect) {
					v = float64(alpha.Pix[alpha.PixOffset(x, y)]) / 255
				}
				if mk.Inverted {
					v = 1 - v
				}
				v *= o
				i := (y-b.Min.Y)*b.Dx() + x - b.Min.X
				switch mk.Mode {
				case "s":
					cover[i] *= 1 - v
				case "i", "d":
					cover[i] *= v
				default: // add, lighten, difference
					cover[i] += v - cover[i]*v
				}
			}
		}
	}
	for i, c := range cover {
		scalePixel(buf.Pix[i*4:i*4+4], c)
	}
}

// applyMatte keeps the parts of buf the matte selects: by its alpha or its
// luminance, or the opposite. A missing matte selects nothing.
func applyMatte(buf, matte *image.RGBA, mode int) {
	for i := 0; i < len(buf.Pix); i += 4 {
		var v float64
		if matte != nil {
			p := matte.Pix[i : i+4]
			switch mode {
			case 3, 4: // premultiplied, so dark and transparent both count as 0
				v = (0.2126*float64(p[0]) + 0.7152*float64(p[1]) + 0.0722*float64(p[2])) / 255 // Rec. 709
			default:
				v = float64(p[3]) / 255
			}
		}
		if mode == 2 || mode == 4 {
			v = 1 - v
		}
		scalePixel(buf.Pix[i:i+4], v)
	}
}

func scaleAlpha(buf *image.RGBA, o float64) {
	if o >= 1 {
		return
	}
	for i := 0; i < len(buf.Pix); i += 4 {
		scalePixel(buf.Pix[i:i+4], o)
	}
}

// scalePixel fades a premultiplied pixel by v.
func scalePixel(p []byte, v float64) {
	if v >= 1 {
		return
	}
	for j := range p {
		p[j] = uint8(float64(p[j])*v + 0.5)
	}
}

func hexColor(s string) (color.NRGBA, bool) {
	s = strings.TrimPrefix(s, "#")
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil || len(s) != 6 {
		return color.NRGBA{}, false
	}
	return color.NRGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 255}, true
}

// entry is the outline of one path shape, after trimming, in pixels.
type entry struct{ paths []polyline }

// trim is a trim paths item and the shapes it applies to: those above it in
// its group and in groups nested there, top one first once reversed.
type trim struct {
	start, end, offset float64
	together           bool
	targets            []*entry
}

// style is a fill or stroke and the shapes it paints, those above it as for
// trims, with the transform and opacity of its group.
type style struct {
	item    *shape
	m       affine
	opacity float64
	entries []*entry
}

// builder walks the shapes of a layer. Items apply to what is above them in
// their group, and to nested groups, so groups are walked from the bottom.
type builder struct {
	frame  float64
	trims  []*trim  // in the order met: outer ones first
	styles []*style // bottom one first, the order they are painted in
	work   *int     // the frame's work, see workBudget
	// active holds the styles and trims that apply where the walk is: those
	// met so far in the groups it is in. Each group pops its own on return.
	activeStyles []*style
	activeTrims  []*trim
}

// spend adds to the frame's work and reports whether it is still in budget.
func (b *builder) spend(n int) bool {
	*b.work += n
	return *b.work <= workBudget
}

func (b *builder) group(items []shape, m affine, opacity float64) {
	if !b.spend(len(items)) { // walked twice, even the hidden ones
		return
	}
	styles, trims := len(b.activeStyles), len(b.activeTrims)
	defer func() { b.activeStyles, b.activeTrims = b.activeStyles[:styles], b.activeTrims[:trims] }()
	for i := range items {
		if it := &items[i]; it.Type == "tr" && !it.Hidden {
			t := it.transform()
			m = m.mul(t.matrix(b.frame))
			opacity *= t.Opacity.scalar(b.frame, 100) / 100
		}
	}
	for i := len(items) - 1; i >= 0; i-- {
		it := &items[i]
		if it.Hidden {
			continue
		}
		switch it.Type {
		case "fl", "st", "gf", "gs":
			s := &style{item: it, m: m, opacity: opacity}
			b.activeStyles = append(b.activeStyles, s)
			b.styles = append(b.styles, s)
		case "tm":
			t := &trim{
				start:    it.S.scalar(b.frame, 0) / 100,
				end:      it.E.scalar(b.frame, 100) / 100,
				offset:   it.O.scalar(b.frame, 0) / 360,
				together: it.TrimMode == 2,
			}
			b.activeTrims = append(b.activeTrims, t)
			b.trims = append(b.trims, t)
		case "gr":
			b.group(it.Items, m, opacity)
		case "sh", "rc", "el":
			// Each style and trim above holds the shape: files can multiply
			// shapes by styles.
			if !b.spend(1 + len(b.activeStyles) + len(b.activeTrims)) {
				return
			}
			e := &entry{paths: []polyline{flatten(b.geometry(it), m)}}
			if !b.spend(len(e.paths[0].pts)) {
				return
			}
			for _, s := range b.activeStyles {
				s.entries = append(s.entries, e)
			}
			for _, t := range b.activeTrims {
				t.targets = append(t.targets, e)
			}
		}
	}
}

// geometry is the path of a shape item in its group's space.
func (b *builder) geometry(it *shape) bezier {
	var p bezier
	switch it.Type {
	case "sh":
		return it.Path.at(b.frame)
	case "rc":
		p = rect(it.P.at(b.frame, 0, 0), it.S.at(b.frame, 0, 0), it.R.scalar(b.frame, 0))
	case "el":
		p = ellipse(it.P.at(b.frame, 0, 0), it.S.at(b.frame, 0, 0))
	}
	if it.reversed() {
		p = p.reverse()
	}
	return p
}

// draw trims the outlines, nearest trim first, then paints every style.
func (b *builder) draw(r *raster, dst *image.RGBA) {
	for _, t := range slices.Backward(b.trims) {
		// A trim walks every point of what it cuts.
		n := len(t.targets)
		for _, e := range t.targets {
			for _, p := range e.paths {
				n += len(p.pts)
			}
		}
		if !b.spend(n) {
			return
		}
		slices.Reverse(t.targets)
		if t.together {
			var all []polyline
			for _, e := range t.targets {
				all = append(all, e.paths...)
			}
			pieces := trimPaths(all, t.start, t.end, t.offset)
			for _, e := range t.targets {
				n := len(e.paths)
				e.paths = slices.Concat(pieces[:n]...)
				pieces = pieces[n:]
			}
			continue
		}
		for _, e := range t.targets {
			var paths []polyline
			for _, p := range e.paths {
				paths = append(paths, trimPaths([]polyline{p}, t.start, t.end, t.offset)[0]...)
			}
			e.paths = paths
		}
	}
	for _, s := range b.styles {
		if !b.spend(len(s.entries)) {
			return
		}
		var paths []polyline
		for _, e := range s.entries {
			paths = append(paths, e.paths...)
		}
		if len(paths) == 0 {
			continue
		}
		it := s.item
		opacity := s.opacity * it.O.scalar(b.frame, 100) / 100
		var src paint
		switch it.Type {
		case "fl", "st":
			c := it.C.at(b.frame, 0, 0, 0)
			if len(c) < 3 {
				continue
			}
			src = solidOf(rgba(c[0], c[1], c[2], opacity))
		default:
			src = newGradient(it, s.m, b.frame, opacity)
		}
		if it.Type == "fl" || it.Type == "gf" {
			fill(r, dst, paths, src)
			continue
		}
		width := it.W.scalar(b.frame, 0) * s.m.scaleFactor()
		stroke(r, dst, paths, width, it.LineCap, it.LineJoin, it.MiterLimit, src)
	}
}

// rgba makes a color from components between 0 and 1. Older files use 0 to
// 255, which shows as components past 1.
func rgba(r, g, b, a float64) color.NRGBA {
	if r > 1 || g > 1 || b > 1 {
		r, g, b = r/255, g/255, b/255
	}
	c := func(v float64) uint8 { return uint8(math.Round(math.Max(0, math.Min(1, v)) * 255)) }
	return color.NRGBA{c(r), c(g), c(b), c(a)}
}

// kappa places bezier tangents that draw a quarter circle.
const kappa = 0.5519150244935105

// ellipse starts at the top and runs clockwise, as After Effects draws it.
func ellipse(c, size []float64) bezier {
	if len(c) < 2 || len(size) < 2 {
		return bezier{}
	}
	rx, ry := size[0]/2, size[1]/2
	kx, ky := rx*kappa, ry*kappa
	return bezier{
		Closed: true,
		V:      []vec{{c[0], c[1] - ry}, {c[0] + rx, c[1]}, {c[0], c[1] + ry}, {c[0] - rx, c[1]}},
		In:     []vec{{-kx, 0}, {0, -ky}, {kx, 0}, {0, ky}},
		Out:    []vec{{kx, 0}, {0, ky}, {-kx, 0}, {0, -ky}},
	}
}

// rect starts at the top right corner and runs clockwise.
func rect(c, size []float64, round float64) bezier {
	if len(c) < 2 || len(size) < 2 {
		return bezier{}
	}
	w, h := size[0]/2, size[1]/2
	l, t, r, btm := c[0]-w, c[1]-h, c[0]+w, c[1]+h
	rd := math.Min(round, math.Min(w, h))
	if rd <= 0 {
		return bezier{Closed: true, V: []vec{{r, t}, {r, btm}, {l, btm}, {l, t}}, In: make([]vec, 4), Out: make([]vec, 4)}
	}
	k := rd * kappa
	return bezier{
		Closed: true,
		V:      []vec{{r, t + rd}, {r, btm - rd}, {r - rd, btm}, {l + rd, btm}, {l, btm - rd}, {l, t + rd}, {l + rd, t}, {r - rd, t}},
		In:     []vec{{0, -k}, {0, 0}, {k, 0}, {0, 0}, {0, k}, {0, 0}, {-k, 0}, {0, 0}},
		Out:    []vec{{0, 0}, {0, k}, {0, 0}, {-k, 0}, {0, 0}, {0, -k}, {0, 0}, {k, 0}},
	}
}

// reverse runs a path the other way, which changes where trims start.
func (p bezier) reverse() bezier {
	n := len(p.V)
	out := bezier{Closed: p.Closed, V: make([]vec, n), In: make([]vec, n), Out: make([]vec, n)}
	for i := range n {
		j := n - 1 - i
		out.V[i] = p.V[j]
		if j < len(p.Out) {
			out.In[i] = p.Out[j]
		}
		if j < len(p.In) {
			out.Out[i] = p.In[j]
		}
	}
	return out
}
