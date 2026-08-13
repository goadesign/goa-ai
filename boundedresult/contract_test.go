package boundedresult

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasContinuation(t *testing.T) {
	empty := ""
	cursor := "next-page"
	tests := []struct {
		name           string
		nextCursor     *string
		refinementHint string
		want           bool
	}{
		{name: "none"},
		{name: "empty cursor", nextCursor: &empty},
		{name: "cursor", nextCursor: &cursor, want: true},
		{name: "refinement", refinementHint: "narrow the query", want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, HasContinuation(test.nextCursor, test.refinementHint))
		})
	}
}
