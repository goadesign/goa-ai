// Search preparation tests pin generated word counts and invalid boundary data.
package tools

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearchDocumentCountsWordsWithoutChangingToolText(t *testing.T) {
	document := NewSearchDocument("reports.find Find reports: find EUR-2026.")
	assert.Equal(t, SearchDocument{
		Length: 7,
		Terms:  map[string]int{"reports": 2, "find": 3, "eur": 1, "2026": 1},
	}, document)
	require.NoError(t, document.Validate())
	require.NoError(t, (SearchDocument{}).Validate())
}

func TestSearchDocumentRejectsInvalidCounts(t *testing.T) {
	for _, document := range []SearchDocument{
		{Length: 1, Terms: map[string]int{"": 1}},
		{Length: 1, Terms: map[string]int{"find": 0}},
		{Length: 1, Terms: map[string]int{"find": -1}},
		{Length: 2, Terms: map[string]int{"find": 1}},
		{Length: 1, Terms: map[string]int{"find": math.MaxInt, "reports": math.MaxInt}},
	} {
		require.Error(t, document.Validate())
	}
}
