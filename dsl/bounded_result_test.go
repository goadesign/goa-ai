package dsl_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// TestBoundedResultRequiresCursorOnlyContinuationField checks that optional
// continuation cursors produce DSL errors and required cursors remain valid.
func TestBoundedResultRequiresCursorOnlyContinuationField(t *testing.T) {
	for _, test := range []struct {
		name     string
		named    bool
		required bool
	}{
		{name: "inline optional"},
		{name: "named optional", named: true},
		{name: "inline required", required: true},
		{name: "named required", named: true, required: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := runDSLWithError(t, func() {
				cursor := func() {
					Attribute("cursor", String, "Progress for the next page.")
					if test.required {
						Required("cursor")
					}
				}
				var continuationArgs any = cursor
				if test.named {
					continuationArgs = Type("ContinuationPayload", cursor)
				}
				Service("svc", func() {
					Agent("agent", "Search helper", func() {
						Use("tools", func() {
							Tool("search", "Search", func() {
								Args(func() {
									Attribute("query", String, "Search query.")
									Required("query")
								})
								Return(func() {
									Attribute("results", ArrayOf(String), "Matching values.")
								})
								BoundedResult(func() {
									ContinueWith("continue_search", "cursor")
									NextCursor("next_cursor")
								})
							})
							Tool("continue_search", "Continue search", func() {
								Args(continuationArgs)
								Return(func() {
									Attribute("results", ArrayOf(String), "Matching values.")
								})
								BoundedResult(func() {
									Cursor("cursor")
									NextCursor("next_cursor")
								})
							})
						})
					})
				})
			})
			if test.required {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, `continuation Args field "cursor" must be required`)
			require.ErrorContains(t, err, `: Args field "cursor" must be required`)
		})
	}
}
