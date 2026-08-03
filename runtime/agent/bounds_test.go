package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCloneBounds(t *testing.T) {
	total := 3
	cursor := "provider-cursor"
	original := &Bounds{
		Returned:       2,
		Total:          &total,
		Truncated:      true,
		NextCursor:     &cursor,
		RefinementHint: "narrow the query",
	}

	cloned := CloneBounds(original)
	require.NotNil(t, cloned)
	assert.Equal(t, original, cloned)
	assert.NotSame(t, original, cloned)
	assert.NotSame(t, original.Total, cloned.Total)
	assert.NotSame(t, original.NextCursor, cloned.NextCursor)

	*cloned.Total = 4
	*cloned.NextCursor = "different-provider-cursor"
	assert.Equal(t, 3, *original.Total)
	assert.Equal(t, "provider-cursor", *original.NextCursor)
}

func TestCloneBoundsNil(t *testing.T) {
	assert.Nil(t, CloneBounds(nil))
}
