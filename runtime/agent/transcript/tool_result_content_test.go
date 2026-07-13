package transcript

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/rawjson"
)

func TestProjectToolResultContentPreservesJSONNumbers(t *testing.T) {
	content, err := ProjectToolResultContent(
		rawjson.Message(`{"reading":9007199254740993}`),
		nil,
		"",
		"",
	)

	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"reading": json.Number("9007199254740993"),
	}, content)
}
