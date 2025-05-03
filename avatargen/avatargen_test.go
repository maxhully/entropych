package avatargen

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGenerateAvatar(t *testing.T) {
	canvas := GenerateAvatar()
	assert.False(t, canvas.Empty())
}

func TestMod(t *testing.T) {
	assert.Equal(t, 0.5, math.Mod(1.5, 1.0))
}
