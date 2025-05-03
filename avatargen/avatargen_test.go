package avatargen

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGenerateAvatar(t *testing.T) {
	result, err := GenerateAvatar()
	assert.Nil(t, err)
	assert.NotNil(t, result)
}

func TestMod(t *testing.T) {
	assert.Equal(t, math.Mod(1.5, 1.0), 0.5)
}
