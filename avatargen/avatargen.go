package avatargen

import (
	"bytes"
	"fmt"
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

type ellipticalArc struct {
	rx     float64
	ry     float64
	rot    float64
	theta0 float64
	theta1 float64
}

type face struct {
	bg       color.Color
	fg       color.Color
	eyeShape ellipticalArc
	mouth    ellipticalArc
	// In [0.0, 1.0]:
	leftEyeX      float64
	eyeSeparation float64
	eyeY          float64
	mouthX        float64
	mouthY        float64
}

func randomFace(width, height float64) face {
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
	return face{
		bg:            hslToRGB(bg),
		fg:            hslToRGB(fg),
		eyeShape:      ellipticalArc{0.05 * width, 0.05 * height, 0.0, 0.0, 180.0},
		mouth:         ellipticalArc{0.1 * width, 0.1 * height, 0.0, 0.0, -180.0},
		leftEyeX:      0.25 * width,
		eyeSeparation: 0.5 * width,
		eyeY:          0.7 * height,
		// Interestingly, this isn't the center:
		mouthX: 0.5 * width,
		mouthY: 0.3 * height,
	}
}

// Maybe eyeShape and mouthShape should just be closures?
func (a *ellipticalArc) Path() *canvas.Path {
	return canvas.EllipticalArc(
		a.rx, a.ry, a.rot, a.theta0, a.theta1,
	)
}

func GenerateAvatar() (image.Image, error) {
	c := canvas.New(256, 256)
	ctx := canvas.NewContext(c)

	face := randomFace(256, 256)
	fmt.Printf("face: %#v\n", face)

	ctx.SetFillColor(face.bg)
	ctx.DrawPath(0.0, 0.0, canvas.Rectangle(256, 256))

	ctx.SetFillColor(face.fg)
	ctx.DrawPath(face.leftEyeX, face.eyeY, face.eyeShape.Path())
	ctx.SetFillColor(face.fg)
	ctx.DrawPath(face.leftEyeX+face.eyeSeparation, face.eyeY, face.eyeShape.Path())
	ctx.SetFillColor(face.fg)
	ctx.DrawPath(face.mouthX, face.mouthY, face.mouth.Path())

	pngWriter := renderers.PNG()
	buf := new(bytes.Buffer)
	if err := pngWriter(buf, c); err != nil {
		return nil, err
	}
	return png.Decode(buf)
}
