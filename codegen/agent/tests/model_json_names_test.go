package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

func TestToolModelJSONNamesUseSnakeCase(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ModelJSONNames())

	specs := fileContent(t, files, "gen/alpha/toolsets/inspect/specs.go")
	require.Contains(t, specs, `device_alias`)
	require.Contains(t, specs, `render_ui`)
	require.Contains(t, specs, `source_ids`)
	require.Contains(t, specs, `time_context`)
	require.Contains(t, specs, `start_time`)
	require.Contains(t, specs, `end_time`)
	require.Contains(t, specs, `result_summary`)
	require.Contains(t, specs, `operator_summaries`)
	require.Contains(t, specs, `user_id`)
	require.Contains(t, specs, `first_name`)
	require.Contains(t, specs, `last_name`)
	require.Contains(t, specs, `ExampleJSON:tools.RawJSON("{\"device_alias\":\"ahu_1\",\"render_ui\":true,\"source_ids\":[\"temp\",\"pressure\"],\"time_context\":{\"end_time\":\"2026-01-01T01:00:00Z\",\"start_time\":\"2026-01-01T00:00:00Z\"}}")`)
	require.NotContains(t, specs, `deviceAlias`)
	require.NotContains(t, specs, `renderUi`)
	require.NotContains(t, specs, `sourceIds`)
	require.NotContains(t, specs, `timeContext`)
	require.NotContains(t, specs, `resultSummary`)
	require.NotContains(t, specs, `operatorSummaries`)
	require.NotContains(t, specs, `userId`)
	require.NotContains(t, specs, `firstName`)
	require.NotContains(t, specs, `lastName`)

	transportTypes := fileContent(t, files, "gen/alpha/toolsets/inspect/http/types.go")
	require.Contains(t, transportTypes, "`json:\"device_alias\"`")
	require.Contains(t, transportTypes, "`json:\"render_ui\"`")
	require.Contains(t, transportTypes, "`json:\"source_ids,omitempty\"`")
	require.Contains(t, transportTypes, "`json:\"time_context\"`")
	require.Contains(t, transportTypes, "`json:\"result_summary\"`")
	require.Contains(t, transportTypes, "`json:\"operator_summaries\"`")
	require.Contains(t, transportTypes, "`json:\"user_id\"`")
	require.Contains(t, transportTypes, "`json:\"first_name\"`")
	require.Contains(t, transportTypes, "`json:\"last_name\"`")

	codecs := fileContent(t, files, "gen/alpha/toolsets/inspect/codecs.go")
	require.Contains(t, codecs, `"device_alias": "Device alias to inspect."`)
	require.Contains(t, codecs, `"render_ui": "Whether the tool should render UI output."`)
	require.Contains(t, codecs, `"time_context.start_time": "Start time for the request."`)
	require.Contains(t, codecs, `"operator_summaries.user_id": "Operator user identifier."`)
	require.Contains(t, codecs, `"device_alias": "string"`)
	require.Contains(t, codecs, `"render_ui": "boolean"`)
	require.Contains(t, codecs, `"source_ids": "array"`)
	require.Contains(t, codecs, `"time_context.start_time": "string"`)
	require.Contains(t, codecs, `"operator_summaries.user_id": "string"`)
}
