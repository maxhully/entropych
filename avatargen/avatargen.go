package avatargen

import (
	"image/color"
	"io"
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

func randInRange(min float64, max float64) float64 {
	return rand.Float64()*(max-min) + min
}

const (
	circle = 0
	smile  = 1
	frown  = 2
)

func randArc(rx, ry float64) ellipticalArc {
	var theta1 float64
	switch face := rand.IntN(3); face {
	case circle:
		theta1 = 360.0
	case smile:
		theta1 = -180.0
	case frown:
		theta1 = 180.0
	}
	// TODO: the rot and theta0 parameters don't work the way I expect them to...
	// Need to debug that.
	return ellipticalArc{rx, ry, 0.0, 0.0, theta1}
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
	eyeShape := randArc(randInRange(0.025, 0.2)*width, randInRange(0.025, 0.15)*height)
	// TODO: do I need to consider eyeShape.ry when chooosing mouth.ry?
	mouth := randArc(randInRange(0.025, 0.6)*width, randInRange(0.025, 0.4)*height)

	// TODO: gotta work on padding and eyeSeparation. A debug visualization showing the
	// rectangle of possible values would be good
	// TODO: I think I want the mouth to overflow more often
	leftEyeX := rand.Float64()*(width-eyeShape.rx*2) + eyeShape.rx
	eyeSeparation := rand.Float64()*(width-leftEyeX-4*eyeShape.rx) + 2*eyeShape.rx
	eyeY := (width+eyeShape.ry)*0.2 + rand.Float64()*0.8*(width-eyeShape.ry)
	// TODO: reserve enough space for the case when the mouth is a circle
	ySpace := eyeY - eyeShape.ry - mouth.ry
	mouthY := rand.Float64()*(ySpace-mouth.ry) + mouth.ry
	return face{
		bg:            hslToRGB(bg),
		fg:            hslToRGB(fg),
		eyeShape:      eyeShape,
		mouth:         mouth,
		leftEyeX:      leftEyeX,
		eyeSeparation: eyeSeparation,
		eyeY:          eyeY,
		mouthX:        width * rand.Float64(),
		mouthY:        mouthY,
	}
}

// Maybe eyeShape and mouthShape should just be closures?
func (a *ellipticalArc) Path() *canvas.Path {
	return canvas.EllipticalArc(
		a.rx, a.ry, a.rot, a.theta0, a.theta1,
	)
}

func GenerateAvatar() *canvas.Canvas {
	c := canvas.New(256, 256)
	ctx := canvas.NewContext(c)

	face := randomFace(256, 256)
	// fmt.Printf("face: %#v\n", face)

	ctx.SetFillColor(face.bg)
	ctx.DrawPath(0.0, 0.0, canvas.Rectangle(256, 256))

	// Adding rx so that the x coordinate is in the center of the ellipse
	// (probably need a similar adjustment for Y)
	ctx.SetFillColor(face.fg)
	ctx.DrawPath(face.leftEyeX+face.eyeShape.rx, face.eyeY, face.eyeShape.Path())
	ctx.SetFillColor(face.fg)
	ctx.DrawPath(face.leftEyeX+face.eyeSeparation+face.eyeShape.rx, face.eyeY, face.eyeShape.Path())
	ctx.SetFillColor(face.fg)
	ctx.DrawPath(face.mouthX+face.mouth.rx, face.mouthY, face.mouth.Path())

	return c
}

func GenerateAvatarPNG(w io.Writer) error {
	c := GenerateAvatar()
	pngWriter := renderers.PNG()
	return pngWriter(w, c)
}
