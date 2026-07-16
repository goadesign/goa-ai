package bedrock

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aws/aws-sdk-go-v2/aws"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	smithy "github.com/aws/smithy-go"
)

// TestPromptTooLongTokenCount_FailsClosed verifies near matches, unrelated
// ValidationExceptions, and non-provider errors are not reinterpreted as
// token counts.
func TestPromptTooLongTokenCount_FailsClosed(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "missing counts",
			err: &brtypes.ValidationException{
				Message: aws.String("prompt is too long"),
			},
		},
		{
			name: "not over maximum",
			err: &brtypes.ValidationException{
				Message: aws.String("prompt is too long: 200000 tokens > 200000 maximum"),
			},
		},
		{
			name: "zero maximum",
			err: &brtypes.ValidationException{
				Message: aws.String("prompt is too long: 1 tokens > 0 maximum"),
			},
		},
		{
			name: "negative input",
			err: &brtypes.ValidationException{
				Message: aws.String("prompt is too long: -1 tokens > 200000 maximum"),
			},
		},
		{
			name: "negative maximum",
			err: &brtypes.ValidationException{
				Message: aws.String("prompt is too long: 1 tokens > -1 maximum"),
			},
		},
		{
			name: "trailing text",
			err: &brtypes.ValidationException{
				Message: aws.String("prompt is too long: 215065 tokens > 200000 maximum (retry)"),
			},
		},
		{
			name: "other validation",
			err: &brtypes.ValidationException{
				Message: aws.String("toolConfig.tools member must not be empty"),
			},
		},
		{
			name: "other API error code",
			err: &brtypes.ValidationException{
				Message:           aws.String("prompt is too long: 215065 tokens > 200000 maximum"),
				ErrorCodeOverride: aws.String("OtherException"),
			},
		},
		{
			name: "plain error with matching text",
			err:  errors.New("prompt is too long: 215065 tokens > 200000 maximum"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			count, ok := promptTooLongTokenCount(test.err)
			require.False(t, ok)
			require.Zero(t, count)
		})
	}
}

// TestPromptTooLongTokenCount_UnwrapsSmithyOperationError verifies the codec
// sees the production AWS error shape rather than requiring a bare modeled
// exception.
func TestPromptTooLongTokenCount_UnwrapsSmithyOperationError(t *testing.T) {
	count, ok := promptTooLongTokenCount(&smithy.OperationError{
		ServiceID:     "Bedrock Runtime",
		OperationName: "CountTokens",
		Err: &brtypes.ValidationException{
			Message: aws.String("prompt is too long: 215065 tokens > 200000 maximum"),
		},
	})

	require.True(t, ok)
	require.Equal(t, 215065, count)
}
