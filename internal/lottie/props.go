package lottie

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
)

// numProp is a number or a vector that may be animated. Lottie writes it as
// {"a":0,"k":value} or {"a":1,"k":[keyframes]}, positions can be split into
// separate x and y properties, and some keys hold a bare value.
type numProp struct {
	keys  []keyframe[[]float64]
	split *[2]numProp
}

// bezier is a path: vertices with in and out tangents relative to them.
type bezier struct {
	Closed bool  `json:"c"`
	V      []vec `json:"v"`
	In     []vec `json:"i"`
	Out    []vec `json:"o"`
}

type vec [2]float64

// pathProp is a path that may be animated, morphing vertex by vertex.
type pathProp struct {
	keys []keyframe[bezier]
}

// keyframe is a value from time t on. Toward the next keyframe it moves along
// an easing curve per dimension, and along a spatial curve for positions.
type keyframe[T any] struct {
	t      float64
	start  T
	end    *T // older files store the end here instead of in the next key
	hold   bool
	easeO  [2][]float64 // out tangent x and y, per dimension
	easeI  [2][]float64
	to, ti []float64
}

func (p *numProp) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		v, err := floats(data)
		p.keys = []keyframe[[]float64]{{start: v}}
		return err
	}
	var obj struct {
		K     json.RawMessage `json:"k"`
		Split bool            `json:"s"`
		X     json.RawMessage `json:"x"`
		Y     json.RawMessage `json:"y"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	if obj.Split {
		p.split = new([2]numProp)
		if err := json.Unmarshal(obj.X, &p.split[0]); err != nil {
			return err
		}
		return json.Unmarshal(obj.Y, &p.split[1])
	}
	var err error
	p.keys, err = parseKeys(obj.K, floats)
	return err
}

func (p *pathProp) UnmarshalJSON(data []byte) error {
	var obj struct {
		K json.RawMessage `json:"k"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	var err error
	p.keys, err = parseKeys(obj.K, path)
	return err
}

// parseKeys reads the "k" of a property: keyframes, or a single value.
func parseKeys[T any](k json.RawMessage, decode func(json.RawMessage) (T, error)) ([]keyframe[T], error) {
	var raw []struct {
		T *float64        `json:"t"`
		S json.RawMessage `json:"s"`
		E json.RawMessage `json:"e"`
		H json.RawMessage `json:"h"`
		O struct {
			X json.RawMessage `json:"x"`
			Y json.RawMessage `json:"y"`
		} `json:"o"`
		I struct {
			X json.RawMessage `json:"x"`
			Y json.RawMessage `json:"y"`
		} `json:"i"`
		To []float64 `json:"to"`
		Ti []float64 `json:"ti"`
	}
	if json.Unmarshal(k, &raw) != nil || len(raw) == 0 || raw[0].T == nil {
		v, err := decode(k)
		return []keyframe[T]{{start: v}}, err
	}
	keys := make([]keyframe[T], 0, len(raw))
	for _, r := range raw {
		if r.T == nil {
			return nil, errors.New("keyframe without time")
		}
		key := keyframe[T]{t: *r.T, hold: string(r.H) == "1" || string(r.H) == "true", to: r.To, ti: r.Ti}
		if len(r.S) > 0 {
			v, err := decode(r.S)
			if err != nil {
				return nil, err
			}
			key.start = v
		} else if len(keys) > 0 && keys[len(keys)-1].end != nil {
			key.start = *keys[len(keys)-1].end // a last key with only a time
		}
		if len(r.E) > 0 {
			v, err := decode(r.E)
			if err != nil {
				return nil, err
			}
			key.end = &v
		}
		key.easeO = [2][]float64{floatsOr(r.O.X), floatsOr(r.O.Y)}
		key.easeI = [2][]float64{floatsOr(r.I.X), floatsOr(r.I.Y)}
		keys = append(keys, key)
	}
	return keys, nil
}

// floats reads a number or an array of numbers.
func floats(data json.RawMessage) ([]float64, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		return nil, nil
	}
	if data[0] == '[' {
		var v []float64
		err := json.Unmarshal(data, &v)
		return v, err
	}
	var v float64
	err := json.Unmarshal(data, &v)
	return []float64{v}, err
}

func floatsOr(data json.RawMessage) []float64 {
	v, _ := floats(data)
	return v
}

// path reads a path, alone or as the only element of an array.
func path(data json.RawMessage) (bezier, error) {
	var b bezier
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '[' {
		var list []bezier
		if err := json.Unmarshal(data, &list); err != nil || len(list) == 0 {
			return b, err
		}
		return list[0], nil
	}
	err := json.Unmarshal(data, &b)
	return b, err
}

// at returns the value at a frame, def when the property is absent.
func (p *numProp) at(frame float64, def ...float64) []float64 {
	if p.split != nil {
		return []float64{p.split[0].scalar(frame, 0), p.split[1].scalar(frame, 0)}
	}
	if len(p.keys) == 0 || len(p.keys[0].start) == 0 {
		return def
	}
	return interpolate(p.keys, frame, func(a, b []float64, ease func(dim int) float64, k *keyframe[[]float64]) []float64 {
		out := make([]float64, min(len(a), len(b)))
		// A position moves along the curve its tangents draw.
		if len(out) >= 2 && len(k.to) >= 2 && len(k.ti) >= 2 && (k.to[0] != 0 || k.to[1] != 0 || k.ti[0] != 0 || k.ti[1] != 0) {
			u := ease(0)
			for d := range 2 {
				out[d] = cubic(a[d], a[d]+k.to[d], b[d]+k.ti[d], b[d], u)
			}
			for d := 2; d < len(out); d++ {
				out[d] = a[d] + (b[d]-a[d])*ease(d)
			}
			return out
		}
		for d := range out {
			out[d] = a[d] + (b[d]-a[d])*ease(d)
		}
		return out
	})
}

// scalar returns the first component of the value at a frame.
func (p *numProp) scalar(frame, def float64) float64 {
	if v := p.at(frame); len(v) > 0 {
		return v[0]
	}
	return def
}

func (p *pathProp) at(frame float64) bezier {
	if len(p.keys) == 0 {
		return bezier{}
	}
	return interpolate(p.keys, frame, func(a, b bezier, ease func(int) float64, _ *keyframe[bezier]) bezier {
		if len(a.V) != len(b.V) || len(a.In) != len(b.In) || len(a.Out) != len(b.Out) {
			return a
		}
		u := ease(0)
		mix := func(x, y []vec) []vec {
			out := make([]vec, len(x))
			for i := range x {
				out[i] = vec{x[i][0] + (y[i][0]-x[i][0])*u, x[i][1] + (y[i][1]-x[i][1])*u}
			}
			return out
		}
		return bezier{Closed: a.Closed, V: mix(a.V, b.V), In: mix(a.In, b.In), Out: mix(a.Out, b.Out)}
	})
}

// interpolate finds the keyframes around a frame and mixes their values with
// mix, given the eased progress of each dimension.
func interpolate[T any](keys []keyframe[T], frame float64, mix func(a, b T, ease func(dim int) float64, k *keyframe[T]) T) T {
	if len(keys) == 1 || frame <= keys[0].t {
		return keys[0].start
	}
	i := len(keys) - 1
	for i > 0 && keys[i].t > frame {
		i--
	}
	k := &keys[i]
	if i == len(keys)-1 {
		return k.start // parseKeys filled a bare last key from the one before
	}
	next := &keys[i+1]
	if k.hold || next.t <= k.t {
		return k.start
	}
	end := next.start
	if k.end != nil {
		end = *k.end
	}
	u := (frame - k.t) / (next.t - k.t)
	ease := func(dim int) float64 {
		pick := func(v []float64, def float64) float64 {
			switch {
			case len(v) == 0:
				return def
			case dim < len(v):
				return v[dim]
			}
			return v[len(v)-1]
		}
		return easing(pick(k.easeO[0], 0), pick(k.easeO[1], 0), pick(k.easeI[0], 1), pick(k.easeI[1], 1), u)
	}
	return mix(k.start, end, ease, k)
}

// easing maps progress u through the cubic bezier from (0,0) to (1,1) with
// control points (x1,y1) and (x2,y2), as CSS timing functions do.
func easing(x1, y1, x2, y2, u float64) float64 {
	if x1 == y1 && x2 == y2 {
		return u
	}
	// Solve x(t) = u by Newton's method, falling back to bisection.
	t := u
	for range 8 {
		x := cubic(0, x1, x2, 1, t) - u
		if math.Abs(x) < 1e-6 {
			return cubic(0, y1, y2, 1, t)
		}
		dx := 3*(1-t)*(1-t)*x1 + 6*(1-t)*t*(x2-x1) + 3*t*t*(1-x2)
		if math.Abs(dx) < 1e-6 {
			break
		}
		t -= x / dx
	}
	lo, hi := 0.0, 1.0
	t = u
	for range 40 {
		if x := cubic(0, x1, x2, 1, t); math.Abs(x-u) < 1e-6 {
			break
		} else if x < u {
			lo = t
		} else {
			hi = t
		}
		t = (lo + hi) / 2
	}
	return cubic(0, y1, y2, 1, t)
}

func cubic(a, b, c, d, t float64) float64 {
	s := 1 - t
	return s*s*s*a + 3*s*s*t*b + 3*s*t*t*c + t*t*t*d
}
