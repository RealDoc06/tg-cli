package lottie

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"slices"

	"golang.org/x/image/vector"
)

// affine maps (x, y) to (a*x + c*y + e, b*x + d*y + f).
type affine struct{ a, b, c, d, e, f float64 }

var identity = affine{a: 1, d: 1}

func translate(x, y float64) affine { return affine{a: 1, d: 1, e: x, f: y} }
func scale(x, y float64) affine     { return affine{a: x, d: y} }

// rotate turns clockwise on screen, by degrees.
func rotate(deg float64) affine {
	s, c := math.Sincos(deg * math.Pi / 180)
	return affine{a: c, b: s, c: -s, d: c}
}

// mul applies n, then m.
func (m affine) mul(n affine) affine {
	return affine{
		a: m.a*n.a + m.c*n.b, b: m.b*n.a + m.d*n.b,
		c: m.a*n.c + m.c*n.d, d: m.b*n.c + m.d*n.d,
		e: m.a*n.e + m.c*n.f + m.e, f: m.b*n.e + m.d*n.f + m.f,
	}
}

func (m affine) apply(p vec) vec {
	return vec{m.a*p[0] + m.c*p[1] + m.e, m.b*p[0] + m.d*p[1] + m.f}
}

func (m affine) invert() affine {
	det := m.a*m.d - m.b*m.c
	if det == 0 {
		return identity
	}
	return affine{
		a: m.d / det, b: -m.b / det, c: -m.c / det, d: m.a / det,
		e: (m.c*m.f - m.d*m.e) / det, f: (m.b*m.e - m.a*m.f) / det,
	}
}

// scaleFactor is how much m enlarges lengths on average, for stroke widths.
func (m affine) scaleFactor() float64 { return math.Sqrt(math.Abs(m.a*m.d - m.b*m.c)) }

// polyline is a path flattened to straight segments, in pixels.
type polyline struct {
	pts    []vec
	closed bool
}

// flatten maps a path to pixels and cuts its curves into short segments.
func flatten(p bezier, m affine) polyline {
	out := polyline{closed: p.Closed}
	n := len(p.V)
	if n == 0 || len(p.In) < n || len(p.Out) < n {
		return out
	}
	tangent := func(v, t vec) vec { return m.apply(vec{v[0] + t[0], v[1] + t[1]}) }
	out.pts = append(out.pts, m.apply(p.V[0]))
	segments := n - 1
	if p.Closed {
		segments = n
	}
	for i := range segments {
		j := (i + 1) % n
		p0, p3 := m.apply(p.V[i]), m.apply(p.V[j])
		p1, p2 := tangent(p.V[i], p.Out[i]), tangent(p.V[j], p.In[j])
		if p1 == p0 && p2 == p3 {
			out.pts = append(out.pts, p3)
			continue
		}
		steps := int(math.Ceil((dist(p0, p1) + dist(p1, p2) + dist(p2, p3)) / 2))
		steps = max(2, min(steps, 64))
		for s := 1; s <= steps; s++ {
			t := float64(s) / float64(steps)
			out.pts = append(out.pts, vec{cubic(p0[0], p1[0], p2[0], p3[0], t), cubic(p0[1], p1[1], p2[1], p3[1], t)})
		}
	}
	if p.Closed && len(out.pts) > 1 && out.pts[len(out.pts)-1] == out.pts[0] {
		out.pts = out.pts[:len(out.pts)-1]
	}
	return out
}

func dist(a, b vec) float64 { return math.Hypot(b[0]-a[0], b[1]-a[1]) }

// raster collects polygons and fills them in one anti aliased pass, over
// their bounding box only. Overlapping polygons merge as with the nonzero
// fill rule. One raster is reused for every shape of a frame.
type raster struct {
	z      *vector.Rasterizer
	polys  [][]vec
	bounds [4]float64 // min x, min y, max x, max y
	cov    []uint8
	// view is the canvas with a margin: polygons are clipped to it, so a
	// point far off canvas costs nothing to rasterize.
	view [4]float64
	// work counts points and layers drawn, see workBudget. Past it, nothing
	// more is drawn.
	work int
}

// layerWork is what a layer counts for in the work budget, pixelWork how
// many pixels count as one unit.
const (
	layerWork = 256
	pixelWork = 64
)

func newRaster(canvas image.Rectangle) *raster {
	return &raster{
		z:    vector.NewRasterizer(1, 1),
		view: [4]float64{float64(canvas.Min.X) - 2, float64(canvas.Min.Y) - 2, float64(canvas.Max.X) + 2, float64(canvas.Max.Y) + 2},
	}
}

func (r *raster) polygon(pts ...vec) {
	if len(pts) < 3 {
		return
	}
	if r.work += len(pts); r.work > workBudget {
		return
	}
	for _, p := range pts {
		if math.IsNaN(p[0]) || math.IsNaN(p[1]) || math.IsInf(p[0], 0) || math.IsInf(p[1], 0) {
			return
		}
	}
	if pts = clip(pts, r.view); len(pts) < 3 {
		return
	}
	if len(r.polys) == 0 {
		r.bounds = [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	}
	for _, p := range pts {
		r.bounds = [4]float64{min(r.bounds[0], p[0]), min(r.bounds[1], p[1]), max(r.bounds[2], p[0]), max(r.bounds[3], p[1])}
	}
	r.polys = append(r.polys, pts)
}

// clip cuts a polygon to a rectangle (min x, min y, max x, max y), one edge
// at a time (Sutherland Hodgman). The coverage inside stays the same.
func clip(pts []vec, box [4]float64) []vec {
	for edge := range 4 {
		inside := func(p vec) bool {
			switch edge {
			case 0:
				return p[0] >= box[0]
			case 1:
				return p[1] >= box[1]
			case 2:
				return p[0] <= box[2]
			}
			return p[1] <= box[3]
		}
		// The crossing lands exactly on the edge, whatever the rounding.
		cross := func(a, b vec) vec {
			if edge == 0 || edge == 2 {
				t := (box[edge] - a[0]) / (b[0] - a[0])
				return vec{box[edge], a[1] + (b[1]-a[1])*t}
			}
			t := (box[edge] - a[1]) / (b[1] - a[1])
			return vec{a[0] + (b[0]-a[0])*t, box[edge]}
		}
		in := pts
		pts = make([]vec, 0, len(in)+4)
		for i, b := range in {
			a := in[(i+len(in)-1)%len(in)]
			switch {
			case inside(b) && !inside(a):
				pts = append(pts, cross(a, b), b)
			case inside(b):
				pts = append(pts, b)
			case inside(a):
				pts = append(pts, cross(a, b))
			}
		}
		if len(pts) == 0 {
			return nil
		}
	}
	return pts
}

// coverage rasterizes the polygons collected so far and forgets them. It
// returns how much of each pixel they cover, inside canvas.
func (r *raster) coverage(canvas image.Rectangle) *image.Alpha {
	defer func() { r.polys = r.polys[:0] }()
	if len(r.polys) == 0 {
		return nil
	}
	box := image.Rect(int(math.Floor(r.bounds[0])), int(math.Floor(r.bounds[1])),
		int(math.Ceil(r.bounds[2]))+1, int(math.Ceil(r.bounds[3]))+1).Intersect(canvas)
	if box.Empty() {
		return nil
	}
	w, h := box.Dx(), box.Dy()
	if r.work += w * h / pixelWork; r.work > workBudget {
		return nil
	}
	r.z.Reset(w, h)
	r.z.DrawOp = draw.Src
	ox, oy := float32(box.Min.X), float32(box.Min.Y)
	for _, pts := range r.polys {
		r.z.MoveTo(float32(pts[0][0])-ox, float32(pts[0][1])-oy)
		for _, p := range pts[1:] {
			r.z.LineTo(float32(p[0])-ox, float32(p[1])-oy)
		}
		r.z.ClosePath()
	}
	if cap(r.cov) < w*h {
		r.cov = make([]uint8, w*h)
	}
	local := &image.Alpha{Pix: r.cov[:w*h], Stride: w, Rect: image.Rect(0, 0, w, h)}
	r.z.Draw(local, local.Rect, image.Opaque, image.Point{})
	local.Rect = box // same pixels, placed where they belong
	return local
}

// paint is a color source: premultiplied components between 0 and 1.
type paint interface {
	at(x, y int) (r, g, b, a float64)
}

// solid is a single color, premultiplied.
type solid [4]float64

func (c solid) at(int, int) (float64, float64, float64, float64) { return c[0], c[1], c[2], c[3] }

func solidOf(c color.NRGBA) solid {
	a := float64(c.A) / 255
	return solid{float64(c.R) / 255 * a, float64(c.G) / 255 * a, float64(c.B) / 255 * a, a}
}

// draw paints the collected polygons over dst. Painting counts toward the
// work budget, a gradient by how many stops it looks through per pixel.
func (r *raster) draw(dst *image.RGBA, src paint) {
	cov := r.coverage(dst.Rect)
	if cov == nil {
		return
	}
	cost := 1
	if g, ok := src.(*gradient); ok {
		cost += len(g.colors)/4 + len(g.alphas)/2
	}
	if r.work += cov.Rect.Dx() * cov.Rect.Dy() * cost / pixelWork; r.work > workBudget {
		return
	}
	b := cov.Rect
	for y := b.Min.Y; y < b.Max.Y; y++ {
		row := cov.Pix[(y-b.Min.Y)*cov.Stride:]
		for x := b.Min.X; x < b.Max.X; x++ {
			c := row[x-b.Min.X]
			if c == 0 {
				continue
			}
			sr, sg, sb, sa := src.at(x, y)
			k := float64(c) / 255
			sr, sg, sb, sa = sr*k, sg*k, sb*k, sa*k
			p := dst.Pix[dst.PixOffset(x, y):]
			keep := 1 - sa
			p[0] = blend(sr, p[0], keep)
			p[1] = blend(sg, p[1], keep)
			p[2] = blend(sb, p[2], keep)
			p[3] = blend(sa, p[3], keep)
		}
	}
}

func blend(src float64, dst uint8, keep float64) uint8 {
	return uint8(math.Min(255, src*255+float64(dst)*keep+0.5))
}

// fill paints the inside of paths, open ones closed by a straight line.
func fill(r *raster, dst *image.RGBA, paths []polyline, src paint) {
	for _, p := range paths {
		r.polygon(p.pts...)
	}
	r.draw(dst, src)
}

// Line caps and joins, as Lottie numbers them.
const (
	capRound     = 2
	capSquare    = 3
	joinMiter    = 1
	joinRound    = 2
	defaultMiter = 4
)

// stroke paints a band of the given width along paths. Every polygon it
// adds winds the same way, so overlapping pieces merge instead of cancelling.
func stroke(r *raster, dst *image.RGBA, paths []polyline, width float64, lineCap, join int, miter float64, src paint) {
	hw := width / 2
	if hw <= 0 {
		return
	}
	if miter <= 0 {
		miter = defaultMiter
	}
	add := func(pts ...vec) {
		if area(pts) > 0 {
			for i, j := 0, len(pts)-1; i < j; i, j = i+1, j-1 {
				pts[i], pts[j] = pts[j], pts[i]
			}
		}
		r.polygon(pts...)
	}
	disc := func(c vec) {
		n := max(8, min(64, int(hw*2)))
		pts := make([]vec, n)
		for i := range pts {
			s, co := math.Sincos(2 * math.Pi * float64(i) / float64(n))
			pts[i] = vec{c[0] + co*hw, c[1] + s*hw}
		}
		add(pts...)
	}
	for _, p := range paths {
		pts := dedupe(p.pts)
		if len(pts) == 1 && lineCap == capRound {
			disc(pts[0])
		}
		if len(pts) < 2 {
			continue
		}
		segments := len(pts) - 1
		if p.closed {
			segments = len(pts)
		}
		normal := func(i int) vec {
			a, b := pts[i%len(pts)], pts[(i+1)%len(pts)]
			l := dist(a, b)
			return vec{-(b[1] - a[1]) / l * hw, (b[0] - a[0]) / l * hw}
		}
		for i := range segments {
			a, b := pts[i], pts[(i+1)%len(pts)]
			n := normal(i)
			add(vec{a[0] + n[0], a[1] + n[1]}, vec{b[0] + n[0], b[1] + n[1]}, vec{b[0] - n[0], b[1] - n[1]}, vec{a[0] - n[0], a[1] - n[1]})
		}
		// Joins fill the wedge left open where two segments meet: at inner
		// vertices, and at every vertex of a closed path.
		first, joins := 1, len(pts)-2
		if p.closed {
			first, joins = 0, len(pts)
		}
		for i := first; i < first+joins; i++ {
			v := pts[i]
			n1, n2 := normal((i+len(pts)-1)%len(pts)), normal(i)
			if join == joinRound {
				disc(v)
				continue
			}
			// Normals are directions turned a quarter, so this is the turn
			// of the path: positive toward the normal's side, which puts
			// the open wedge on the other side.
			side := 1.0
			if n1[0]*n2[1]-n1[1]*n2[0] > 0 {
				side = -1
			}
			o1 := vec{v[0] + side*n1[0], v[1] + side*n1[1]}
			o2 := vec{v[0] + side*n2[0], v[1] + side*n2[1]}
			if join == joinMiter {
				bis := vec{n1[0] + n2[0], n1[1] + n2[1]}
				if l := math.Hypot(bis[0], bis[1]); l > 1e-9 {
					cosHalf := (bis[0]*n1[0] + bis[1]*n1[1]) / l / hw
					if cosHalf > 1e-9 && 1/cosHalf <= miter {
						k := side * hw / cosHalf / l
						add(v, o1, vec{v[0] + bis[0]*k, v[1] + bis[1]*k}, o2)
						continue
					}
				}
			}
			add(v, o1, o2) // bevel
		}
		if p.closed {
			continue
		}
		for _, end := range [2]struct{ at, from vec }{{pts[0], pts[1]}, {pts[len(pts)-1], pts[len(pts)-2]}} {
			switch lineCap {
			case capRound:
				disc(end.at)
			case capSquare:
				l := dist(end.from, end.at)
				d := vec{(end.at[0] - end.from[0]) / l * hw, (end.at[1] - end.from[1]) / l * hw}
				n := vec{-d[1], d[0]}
				e := end.at
				add(vec{e[0] + n[0], e[1] + n[1]}, vec{e[0] + n[0] + d[0], e[1] + n[1] + d[1]},
					vec{e[0] - n[0] + d[0], e[1] - n[1] + d[1]}, vec{e[0] - n[0], e[1] - n[1]})
			}
		}
	}
	r.draw(dst, src)
}

// area is the signed area of a polygon, positive when it runs clockwise on
// screen.
func area(pts []vec) float64 {
	var s float64
	for i, p := range pts {
		q := pts[(i+1)%len(pts)]
		s += p[0]*q[1] - q[0]*p[1]
	}
	return s / 2
}

// dedupe drops repeated points, which have no direction to stroke along.
func dedupe(pts []vec) []vec {
	out := make([]vec, 0, len(pts))
	for _, p := range pts {
		if len(out) == 0 || dist(out[len(out)-1], p) > 1e-6 {
			out = append(out, p)
		}
	}
	return out
}

// trimPaths keeps the part of paths between start and end, fractions of
// their total length shifted by offset, wrapping around. It returns the
// pieces of each path in order.
func trimPaths(paths []polyline, start, end, offset float64) [][]polyline {
	out := make([][]polyline, len(paths))
	// Eased keyframes can overshoot, which must not wrap to the other end.
	start, end = math.Max(0, math.Min(1, start)), math.Max(0, math.Min(1, end))
	if start > end {
		start, end = end, start
	}
	if end-start >= 1 {
		for i, p := range paths {
			out[i] = []polyline{p}
		}
		return out
	}
	start, end = start+offset, end+offset
	shift := math.Floor(start)
	start, end = start-shift, end-shift
	ranges := [][2]float64{{start, math.Min(end, 1)}}
	if end > 1 {
		ranges = append(ranges, [2]float64{0, end - 1})
	}

	lengths := make([]float64, len(paths))
	var total float64
	for i, p := range paths {
		lengths[i] = p.length()
		total += lengths[i]
	}
	if total == 0 || end == start {
		return out
	}
	var at float64
	for i, p := range paths {
		from, to := at/total, (at+lengths[i])/total
		at += lengths[i]
		if lengths[i] > 0 && slices.ContainsFunc(ranges, func(r [2]float64) bool { return r[0] <= from && to <= r[1] }) {
			out[i] = []polyline{p} // kept whole, a closed path stays closed
			continue
		}
		var pieces []polyline
		for _, r := range ranges {
			a, b := math.Max(r[0], from), math.Min(r[1], to)
			if b > a && lengths[i] > 0 {
				pieces = append(pieces, p.sub((a-from)*total, (b-from)*total))
			}
		}
		// A closed path cut across its start is one piece, not two.
		if p.closed && len(pieces) == 2 && len(ranges) == 2 {
			pieces = []polyline{{pts: append(pieces[0].pts, pieces[1].pts[1:]...)}}
		}
		out[i] = pieces
	}
	return out
}

func (p polyline) length() float64 {
	var l float64
	for i := 1; i < len(p.pts); i++ {
		l += dist(p.pts[i-1], p.pts[i])
	}
	if p.closed && len(p.pts) > 1 {
		l += dist(p.pts[len(p.pts)-1], p.pts[0])
	}
	return l
}

// sub is the open stretch of p between two distances along it.
func (p polyline) sub(from, to float64) polyline {
	pts := p.pts
	if p.closed && len(pts) > 0 {
		pts = append(pts[:len(pts):len(pts)], pts[0])
	}
	var out polyline
	var at float64
	for i := 1; i < len(pts); i++ {
		a, b := pts[i-1], pts[i]
		l := dist(a, b)
		if l == 0 {
			continue
		}
		lerp := func(d float64) vec {
			t := (d - at) / l
			return vec{a[0] + (b[0]-a[0])*t, a[1] + (b[1]-a[1])*t}
		}
		if at+l >= from && at <= to {
			if len(out.pts) == 0 {
				out.pts = append(out.pts, lerp(math.Max(from, at)))
			}
			out.pts = append(out.pts, lerp(math.Min(to, at+l)))
		}
		at += l
	}
	return out
}

// gradient is a linear or radial gradient.
type gradient struct {
	inv        affine // pixels to the gradient's space
	start, end vec
	radial     bool
	colors     []float64 // offset, r, g, b per stop
	alphas     []float64 // offset, alpha per stop
	opacity    float64
}

func newGradient(it *shape, m affine, frame, opacity float64) *gradient {
	s, e := it.S.at(frame, 0, 0), it.E.at(frame, 0, 0)
	g := &gradient{inv: m.invert(), radial: it.GradientType == 2, opacity: opacity}
	if len(s) >= 2 && len(e) >= 2 {
		g.start, g.end = vec{s[0], s[1]}, vec{e[0], e[1]}
	}
	values := it.Gradient.Colors.at(frame)
	n := max(0, min(it.Gradient.Count*4, len(values)))
	g.colors, g.alphas = values[:n-n%4], values[n:]
	return g
}

func (g *gradient) at(x, y int) (float64, float64, float64, float64) {
	p := g.inv.apply(vec{float64(x) + 0.5, float64(y) + 0.5})
	d := vec{g.end[0] - g.start[0], g.end[1] - g.start[1]}
	l2 := d[0]*d[0] + d[1]*d[1]
	var t float64
	switch {
	case l2 == 0:
	case g.radial:
		t = dist(g.start, p) / math.Sqrt(l2)
	default:
		t = ((p[0]-g.start[0])*d[0] + (p[1]-g.start[1])*d[1]) / l2
	}
	t = math.Max(0, math.Min(1, t))
	if len(g.colors) < 4 {
		return 0, 0, 0, 0
	}
	r, gr, b := stops(g.colors, 4, t, 1), stops(g.colors, 4, t, 2), stops(g.colors, 4, t, 3)
	if r > 1 || gr > 1 || b > 1 {
		r, gr, b = r/255, gr/255, b/255
	}
	a := g.opacity
	if len(g.alphas) >= 2 {
		a *= stops(g.alphas, 2, t, 1)
	}
	a = math.Max(0, math.Min(1, a))
	return r * a, gr * a, b * a, a
}

// stops interpolates component k of gradient stops laid out stride apart,
// each starting with its offset.
func stops(v []float64, stride int, t float64, k int) float64 {
	n := len(v) / stride
	if t <= v[0] || n == 1 {
		return v[k]
	}
	for i := 1; i < n; i++ {
		o0, o1 := v[(i-1)*stride], v[i*stride]
		if t <= o1 {
			if o1 <= o0 {
				return v[i*stride+k]
			}
			u := (t - o0) / (o1 - o0)
			return v[(i-1)*stride+k] + (v[i*stride+k]-v[(i-1)*stride+k])*u
		}
	}
	return v[(n-1)*stride+k]
}
