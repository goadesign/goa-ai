package vertex

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewAnthropicClientValidates(t *testing.T) {
	_, err := NewAnthropicClient(context.Background(), AnthropicOptions{Region: "global"})
	require.Error(t, err) // missing project
	_, err = NewAnthropicClient(context.Background(), AnthropicOptions{ProjectID: "p"})
	require.Error(t, err) // missing region
	_, err = NewAnthropicClient(context.Background(), AnthropicOptions{ProjectID: "p", Region: "global"})
	require.Error(t, err) // missing default model
}
