package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/rawjson"
)

func TestContinuationRetainsOnlyDeclaredFieldsFromEachChain(t *testing.T) {
	_, spec := continuationTestSpecs()
	spec.Bounds.Paging.RetainedFields = []string{"workspace", "optional"}
	before := cloneToolSpec(spec)
	for _, test := range []struct{ input, cursor, want string }{
		{`{"workspace":{"name":"one"},"query":"first","cursor":"old"}`, "one-next", `{"workspace":{"name":"one"},"cursor":"one-next"}`},
		{`{"workspace":{"name":"two"},"query":"second","optional":false}`, "two-next", `{"workspace":{"name":"two"},"optional":false,"cursor":"two-next"}`},
	} {
		got, err := continuationPayload(spec, continuationState{payload: rawjson.Message(test.input), cursor: test.cursor})
		require.NoError(t, err)
		assert.JSONEq(t, test.want, string(got))
	}
	assert.Equal(t, before.Bounds, spec.Bounds)
	copied := cloneToolSpec(spec)
	copied.Bounds.Paging.RetainedFields[0] = "changed"
	assert.Equal(t, "workspace", spec.Bounds.Paging.RetainedFields[0])
}
