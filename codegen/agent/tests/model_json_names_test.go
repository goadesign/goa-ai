package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

func TestToolModelJSONNamesUseSnakeCase(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ModelJSONNames())

	specs := fileContent(t, files, "gen/alpha/toolsets/review/specs.go")
	require.Contains(t, specs, `record_key`)
	require.Contains(t, specs, `include_details`)
	require.Contains(t, specs, `source_ids`)
	require.Contains(t, specs, `time_context`)
	require.Contains(t, specs, `start_time`)
	require.Contains(t, specs, `end_time`)
	require.Contains(t, specs, `summary_text`)
	require.Contains(t, specs, `reviewer_summaries`)
	require.Contains(t, specs, `user_id`)
	require.Contains(t, specs, `first_name`)
	require.Contains(t, specs, `last_name`)
	require.Contains(t, specs, `ExampleJSON:tools.RawJSON("{\"include_details\":true,\"record_key\":\"record_1\",\"source_ids\":[\"source_1\",\"source_2\"],\"time_context\":{\"end_time\":\"2026-01-01T01:00:00Z\",\"start_time\":\"2026-01-01T00:00:00Z\"}}")`)
	require.NotContains(t, specs, `recordKey`)
	require.NotContains(t, specs, `includeDetails`)
	require.NotContains(t, specs, `sourceIds`)
	require.NotContains(t, specs, `timeContext`)
	require.NotContains(t, specs, `summaryText`)
	require.NotContains(t, specs, `reviewerSummaries`)
	require.NotContains(t, specs, `userId`)
	require.NotContains(t, specs, `firstName`)
	require.NotContains(t, specs, `lastName`)

	transportTypes := fileContent(t, files, "gen/alpha/toolsets/review/http/types.go")
	require.Contains(t, transportTypes, "`json:\"record_key\"`")
	require.Contains(t, transportTypes, "`json:\"include_details\"`")
	require.Contains(t, transportTypes, "`json:\"source_ids,omitempty\"`")
	require.Contains(t, transportTypes, "`json:\"time_context\"`")
	require.Contains(t, transportTypes, "`json:\"summary_text\"`")
	require.Contains(t, transportTypes, "`json:\"reviewer_summaries\"`")
	require.Contains(t, transportTypes, "`json:\"user_id\"`")
	require.Contains(t, transportTypes, "`json:\"first_name\"`")
	require.Contains(t, transportTypes, "`json:\"last_name\"`")

	codecs := fileContent(t, files, "gen/alpha/toolsets/review/codecs.go")
	require.Contains(t, codecs, `tools.FixedField("record_key")`)
	require.Contains(t, codecs, `tools.FixedField("include_details")`)
	require.Contains(t, codecs, `tools.FixedField("time_context")`)
	require.Contains(t, codecs, `tools.FixedField("reviewer_summaries")`)
	require.Contains(t, codecs, `tools.DynamicField{}`)
	require.Contains(t, codecs, `Description: "Record key to review."`)
	require.Contains(t, codecs, `Description: "Start time for the request."`)
	require.Contains(t, codecs, `Description: "Reviewer identifier."`)
	require.Contains(t, codecs, `JSONType: "string"`)
	require.Contains(t, codecs, `JSONType: "boolean"`)
	require.Contains(t, codecs, `JSONType: "array"`)
}
