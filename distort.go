package entropy

import (
	mathrand "math/rand"
	"strings"
)

func randomContentRune() rune {
	// TODO: find more fun content ranges to include in the noise
	// This is the "Basic Latin" range of code points
	minRune := 0x0020
	maxRune := 0x007F

	// The "fun zone" of unicode:
	//
	// 2580 — 259F	Block Elements
	// 25A0 — 25FF	Geometric Shapes
	// 2600 — 26FF	Miscellaneous Symbols
	// 2700 — 27BF	Dingbats
	if mathrand.Float32() < 0.3 {
		minRune = 0x2580
		maxRune = 0x27BF
	}

	i := mathrand.Intn(maxRune - minRune)
	return rune(minRune + i)
}

const MaxDistortionLevel = 5

func DistortContent(content string, graphDistance int) string {
	if graphDistance == 0 {
		return content
	}
	var builder strings.Builder
	builder.Grow(len(content))

	// TODO: I think I need to make this subtler. The jump from 2 to 3 is crazy
	p := min(float32(graphDistance-1)/float32(2*MaxDistortionLevel), 1.0)
	if p == 0.0 {
		p = 0.01
	}

	// TODO: wrap the noise in <mark> tags in a different style?
	for _, r := range content {
		if mathrand.Float32() > p {
			builder.WriteRune(r)
		} else {
			builder.WriteRune(randomContentRune())
		}
	}
	return builder.String()
}
