// Package bedrock tests provider-specific numeric limits that are narrower
// than the provider-neutral model request.
package bedrock

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBedrockInt32RejectsMaxInt(t *testing.T) {
	_, err := bedrockInt32("max tokens", math.MaxInt)

	require.EqualError(t, err, "bedrock: max tokens must be between 0 and 2147483647")
}
