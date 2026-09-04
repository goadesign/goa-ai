// Package model tests the provider-neutral message contract.
package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMessageTextPreservesDisplayParts(t *testing.T) {
	message := &Message{Parts: []Part{
		TextPart{Text: "answer "},
		ThinkingPart{Text: "private reasoning"},
		CitationsPart{Text: "with sources"},
		ToolUsePart{ID: "call-1", Name: "lookup"},
	}}

	require.Equal(t, "answer with sources", message.Text())
}
