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

	// TODO: I think I need to make this subtler
	p := min(float32(graphDistance-1)/float32(MaxDistortionLevel+1), 1.0)
	if p == 0.0 {
		p = 0.05
	}

	// TODO: wrap the errors in <mark> tags in a different style
	for _, r := range content {
		if mathrand.Float32() > p {
			builder.WriteRune(r)
		} else {
			builder.WriteRune(randomContentRune())
		}
	}
	return builder.String()
}
