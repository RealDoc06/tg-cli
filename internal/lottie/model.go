// Package lottie renders Lottie animations, the format of Telegram's animated
// stickers (a TGS file is gzipped Lottie JSON). It draws shape, solid, null
// and precomp layers, parenting, masks, track mattes, and shapes with fills,
// strokes, gradients and trim paths. It skips what Telegram forbids in
// stickers (effects, expressions, images, text, 3D, stars, repeaters, time
// remapping) and some of what it allows: even-odd fills draw as nonzero,
// merge paths and dashes are ignored, and precomps are not clipped. Files
// come from strangers, so rendering is bounded, see ErrTooComplex.
package lottie

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Animation is a parsed Lottie file. Times are in frames, at FrameRate.
type Animation struct {
	FrameRate float64 `json:"fr"`
	In        float64 `json:"ip"`
	Out       float64 `json:"op"`
	Width     float64 `json:"w"`
	Height    float64 `json:"h"`
	Layers    []layer `json:"layers"`
	Assets    []struct {
		ID     string  `json:"id"`
		Layers []layer `json:"layers"`
	} `json:"assets"`

	precomps map[string][]layer
}

// Parse reads a Lottie animation from its JSON.
func Parse(data []byte) (*Animation, error) {
	var a Animation
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, fmt.Errorf("lottie: %w", err)
	}
	if a.Width <= 0 || a.Height <= 0 || a.FrameRate <= 0 || a.Out <= a.In {
		return nil, errors.New("lottie: no size, frame rate or duration")
	}
	a.precomps = map[string][]layer{}
	for _, asset := range a.Assets {
		if asset.Layers != nil {
			a.precomps[asset.ID] = asset.Layers
		}
	}
	return &a, nil
}

// Layer types.
const (
	layerPrecomp = 0
	layerSolid   = 1
	layerNull    = 3
	layerShape   = 4
)

type layer struct {
	Type    int       `json:"ty"`
	Index   *int      `json:"ind"`
	Parent  *int      `json:"parent"`
	In      float64   `json:"ip"`
	Out     float64   `json:"op"`
	Start   float64   `json:"st"`
	Stretch float64   `json:"sr"`
	Hidden  bool      `json:"hd"`
	Ks      transform `json:"ks"`
	Shapes  []shape   `json:"shapes"`
	RefID   string    `json:"refId"`
	Masks   []mask    `json:"masksProperties"`
	// MatteSource is 1 for a layer that only serves as the track matte of
	// another. MatteMode is the matte this layer uses: 1 alpha, 2 inverted
	// alpha, 3 luma, 4 inverted luma. The matte is the layer MatteParent
	// names, or the one above when it is not set.
	MatteSource int     `json:"td"`
	MatteMode   int     `json:"tt"`
	MatteParent *int    `json:"tp"`
	SolidColor  string  `json:"sc"`
	SolidWidth  float64 `json:"sw"`
	SolidHeight float64 `json:"sh"`
}

func (l *layer) visible(frame float64) bool {
	return !l.Hidden && frame >= l.In && frame < l.Out
}

// transform is a layer's or a shape group's transform.
type transform struct {
	Anchor   numProp `json:"a"`
	Position numProp `json:"p"`
	Scale    numProp `json:"s"`
	Rotation numProp `json:"r"`
	Opacity  numProp `json:"o"`
	Skew     numProp `json:"sk"`
	SkewAxis numProp `json:"sa"`
}

type mask struct {
	Mode     string   `json:"mode"`
	Inverted bool     `json:"inv"`
	Path     pathProp `json:"pt"`
	Opacity  numProp  `json:"o"`
}

// shape is any item of a shape layer, told apart by Type: "gr" group, "sh"
// path, "rc" rectangle, "el" ellipse, "fl" fill, "st" stroke, "gf" gradient
// fill, "gs" gradient stroke, "tm" trim paths, "tr" the group's transform.
// Lottie reuses keys across types, so the fields are named after the keys.
type shape struct {
	Type   string   `json:"ty"`
	Hidden bool     `json:"hd"`
	Items  []shape  `json:"it"`
	Path   pathProp `json:"ks"`

	A  numProp `json:"a"`  // anchor (tr), highlight angle (gf)
	P  numProp `json:"p"`  // position, center (rc, el)
	S  numProp `json:"s"`  // scale (tr), size (rc, el), start (tm, gf)
	E  numProp `json:"e"`  // end (tm, gf)
	R  numProp `json:"r"`  // rotation (tr), roundness (rc), fill rule (fl)
	O  numProp `json:"o"`  // opacity, offset (tm)
	C  numProp `json:"c"`  // color
	W  numProp `json:"w"`  // stroke width
	Sk numProp `json:"sk"` // skew (tr)
	Sa numProp `json:"sa"` // skew axis (tr)

	Gradient struct {
		Count  int     `json:"p"`
		Colors numProp `json:"k"`
	} `json:"g"`
	GradientType int     `json:"t"` // 1 linear, 2 radial
	TrimMode     int     `json:"m"` // 1 each path on its own, 2 all as one
	LineCap      int     `json:"lc"`
	LineJoin     int     `json:"lj"`
	MiterLimit   float64 `json:"ml"`
	// Direction is 3 for a reversed rectangle or ellipse. It is raw because
	// strokes use the same key for dashes.
	Direction json.RawMessage `json:"d"`
}

func (s *shape) transform() transform {
	return transform{Anchor: s.A, Position: s.P, Scale: s.S, Rotation: s.R, Opacity: s.O, Skew: s.Sk, SkewAxis: s.Sa}
}

func (s *shape) reversed() bool { return string(s.Direction) == "3" }
