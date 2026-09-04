package tools

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLookupFieldMetadata(t *testing.T) {
	metadata := []FieldMetadata{
		{Path: []FieldPathSegment{FixedField("labels"), DynamicField{}, FixedField("name")}, Description: "map name"},
		{Path: []FieldPathSegment{FixedField("labels"), DynamicField{}}, Description: "map value"},
		{Path: []FieldPathSegment{FixedField("labels"), FixedField("fixed")}, Description: "exact"},
	}

	value, ok := LookupFieldMetadata(metadata, "labels.room.name")
	require.True(t, ok)
	require.Equal(t, "map name", value.Description)

	value, ok = LookupFieldMetadata(metadata, "/labels/fixed")
	require.True(t, ok)
	require.Equal(t, "exact", value.Description)

	value, ok = LookupFieldMetadata(metadata, "/labels/room~1east/name")
	require.True(t, ok)
	require.Equal(t, "map name", value.Description)

	value, ok = LookupFieldMetadata(metadata, "/labels/room~0east")
	require.True(t, ok)
	require.Equal(t, "map value", value.Description)

	_, ok = LookupFieldMetadata(metadata, "/labels/room~2east")
	require.False(t, ok)
}

func TestLookupFieldMetadataKeepsFixedNamesDistinctFromPathSyntax(t *testing.T) {
	metadata := []FieldMetadata{
		{Path: []FieldPathSegment{FixedField("a.b")}, Description: "literal dot"},
		{Path: []FieldPathSegment{FixedField("a"), FixedField("b")}, Description: "nested"},
		{Path: []FieldPathSegment{FixedField("items"), FixedField("*")}, Description: "literal star"},
		{Path: []FieldPathSegment{FixedField("items"), DynamicField{}}, Description: "dynamic key"},
	}

	literalDot, ok := LookupFieldMetadata(metadata, "/a.b")
	require.True(t, ok)
	require.Equal(t, "literal dot", literalDot.Description)
	nested, ok := LookupFieldMetadata(metadata, "/a/b")
	require.True(t, ok)
	require.Equal(t, "nested", nested.Description)
	literalStar, ok := LookupFieldMetadata(metadata, "/items/*")
	require.True(t, ok)
	require.Equal(t, "literal star", literalStar.Description)

	require.Equal(t, `["a.b"]`, FieldPathString(literalDot.Path))
	require.Equal(t, "a.b", FieldPathString(nested.Path))
}

func TestLookupFieldMetadataRejectsEqualSpecificityMatches(t *testing.T) {
	metadata := []FieldMetadata{
		{
			Path:        []FieldPathSegment{FixedField("labels"), DynamicField{}, FixedField("name")},
			Description: "dynamic label",
		},
		{
			Path:        []FieldPathSegment{FixedField("labels"), FixedField("room"), DynamicField{}},
			Description: "dynamic room field",
		},
	}

	_, ok := LookupFieldMetadata(metadata, "/labels/room/name")
	require.False(t, ok)
}

func TestCloneFieldMetadataCopiesNestedSlices(t *testing.T) {
	original := []FieldMetadata{{
		Path:                []FieldPathSegment{FixedField("choice"), FixedField("value")},
		DiscriminatorValues: []string{"one"},
		Branches: []UnionBranch{{
			Discriminator: []FieldPathSegment{FixedField("choice"), FixedField("type")},
			Value:         "one",
		}},
	}}
	cloned := CloneFieldMetadata(original)
	cloned[0].Path[0] = FixedField("changed")
	cloned[0].DiscriminatorValues[0] = "changed"
	cloned[0].Branches[0].Discriminator[0] = FixedField("changed")

	require.Equal(t, FixedField("choice"), original[0].Path[0])
	require.Equal(t, "one", original[0].DiscriminatorValues[0])
	require.Equal(t, FixedField("choice"), original[0].Branches[0].Discriminator[0])
}
