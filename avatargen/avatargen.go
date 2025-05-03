package avatargen

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/rand/v2"

	"github.com/tdewolff/canvas"
	"github.com/tdewolff/canvas/renderers"
)

// HSL conversion code from
// https://github.com/gerow/go-color/blob/master/color.go
func hueToRGB(v1, v2, h float64) float64 {
	if h < 0 {
		h += 1
	}
	if h > 1 {
		h -= 1
	}
	switch {
	case 6*h < 1:
		return (v1 + (v2-v1)*6*h)
	case 2*h < 1:
		return v2
	case 3*h < 2:
		return v1 + (v2-v1)*((2.0/3.0)-h)*6
	}
	return v1
}

type hsl struct {
	h, s, l float64
}

func hslToRGB(c hsl) color.Color {
	h, s, l := c.h, c.h, c.l

	var v1, v2 float64
	if l < 0.5 {
		v2 = l * (1 + s)
	} else {
		v2 = (l + s) - (s * l)
	}

	v1 = 2*l - v2

	r := hueToRGB(v1, v2, h+(1.0/3.0))
	g := hueToRGB(v1, v2, h)
	b := hueToRGB(v1, v2, h-(1.0/3.0))

	return color.RGBA{
		R: uint8(math.Round(r * 255)),
		G: uint8(math.Round(g * 255)),
		B: uint8(math.Round(b * 255)),
		A: 255,
	}
}

func GenerateAvatar() (image.Image, error) {
	c := canvas.New(256, 256)
	ctx := canvas.NewContext(c)

	l := rand.Float64()*0.7 + 0.2

	bg := hsl{
		rand.Float64(),
		rand.Float64()*0.2 + 0.8,
		math.Pow(l, 1.0/3.0),
	}
	fg := hsl{
		math.Mod(bg.h+0.5, 1.0),
		0.95,
		math.Pow(l, 3.0),
	}

	ctx.SetFillColor(hslToRGB(bg))
	ctx.DrawPath(0.0, 0.0, canvas.Rectangle(256, 256))

	ctx.SetFillColor(hslToRGB(fg))
	ctx.DrawPath(50.0, 50.0, canvas.Ellipse(10.0, 10.0))

	pngWriter := renderers.PNG()
	buf := new(bytes.Buffer)
	if err := pngWriter(buf, c); err != nil {
		return nil, err
	}
	return png.Decode(buf)
}
