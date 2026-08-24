package tools

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLookupFieldMetadata(t *testing.T) {
	metadata := map[string]string{
		"labels.*.name": "map name",
		"labels.*":      "map value",
		"labels.fixed":  "exact",
	}

	value, ok := LookupFieldMetadata(metadata, "labels.room.name")
	require.True(t, ok)
	require.Equal(t, "map name", value)

	value, ok = LookupFieldMetadata(metadata, "labels.fixed")
	require.True(t, ok)
	require.Equal(t, "exact", value)

	value, ok = LookupFieldMetadata(metadata, "/labels/fixed")
	require.True(t, ok)
	require.Equal(t, "exact", value)

	value, ok = LookupFieldMetadata(metadata, "/labels/room~1east/name")
	require.True(t, ok)
	require.Equal(t, "map name", value)

	value, ok = LookupFieldMetadata(
		map[string]string{"labels.*": "escaped map value"},
		"/labels/room~0east",
	)
	require.True(t, ok)
	require.Equal(t, "escaped map value", value)

	_, ok = LookupFieldMetadata(metadata, "/labels/room~2east")
	require.False(t, ok)

	_, ok = LookupFieldMetadata(map[string]string{
		"labels.*.name": "first",
		"labels.room.*": "second",
	}, "labels.room.name")
	require.False(t, ok)
}
