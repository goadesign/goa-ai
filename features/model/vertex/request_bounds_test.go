// Package vertex tests provider-specific numeric limits that are narrower
// than the provider-neutral model request.
package vertex

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVertexInt32RejectsMaxInt(t *testing.T) {
	_, err := vertexInt32("max tokens", math.MaxInt)

	require.EqualError(t, err, "vertex: max tokens must be between 0 and 2147483647")
}
