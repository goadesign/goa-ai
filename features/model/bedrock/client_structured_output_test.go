package bedrock

import (
	"testing"

	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestEncodeOutputConfigStructuredOutput(t *testing.T) {
	cfg, err := encodeOutputConfig(&model.StructuredOutput{
		Schema: []byte(`{"type":"object","required":["assistant_text"]}`),
		Name:   "structured_output",
	})
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.NotNil(t, cfg.TextFormat)
	require.Equal(t, "json_schema", string(cfg.TextFormat.Type))
	member, ok := cfg.TextFormat.Structure.(*brtypes.OutputFormatStructureMemberJsonSchema)
	require.True(t, ok)
	require.NotNil(t, member.Value.Schema)
	require.JSONEq(t, `{"type":"object","required":["assistant_text"]}`, *member.Value.Schema)
	require.NotNil(t, member.Value.Name)
	require.Equal(t, "structured_output", *member.Value.Name)
}

func TestEncodeOutputConfigRejectsMissingSchema(t *testing.T) {
	_, err := encodeOutputConfig(&model.StructuredOutput{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires a schema")
}
