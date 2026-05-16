package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
)

// GenAI semantic-convention attributes used by goa-ai. The constants live in
// the runtime telemetry package so open-source integrations can emit standard
// OpenTelemetry fields without depending on a vendor-specific backend.
const (
	GenAIOperationChat        = "chat"
	GenAIOperationExecuteTool = "execute_tool"
	GenAIOperationInvokeAgent = "invoke_agent"

	GenAIProviderAWSBedrock = "aws.bedrock"
)

// GenAI semantic-convention attribute keys.
const (
	AttrGenAIConversationID          = attribute.Key("gen_ai.conversation.id")
	AttrGenAIAgentID                 = attribute.Key("gen_ai.agent.id")
	AttrGenAIAgentName               = attribute.Key("gen_ai.agent.name")
	AttrGenAIOperationName           = attribute.Key("gen_ai.operation.name")
	AttrGenAIProviderName            = attribute.Key("gen_ai.provider.name")
	AttrGenAIRequestModel            = attribute.Key("gen_ai.request.model")
	AttrGenAIRequestMaxTokens        = attribute.Key("gen_ai.request.max_tokens")
	AttrGenAIResponseModel           = attribute.Key("gen_ai.response.model")
	AttrGenAIResponseFinishReasons   = attribute.Key("gen_ai.response.finish_reasons")
	AttrGenAIResponseTTFT            = attribute.Key("gen_ai.response.time_to_first_chunk")
	AttrGenAIUsageInputTokens        = attribute.Key("gen_ai.usage.input_tokens")
	AttrGenAIUsageOutputTokens       = attribute.Key("gen_ai.usage.output_tokens")
	AttrGenAIUsageCacheReadTokens    = attribute.Key("gen_ai.usage.cache_read.input_tokens")
	AttrGenAIUsageCacheCreationToken = attribute.Key("gen_ai.usage.cache_creation.input_tokens")
	AttrGenAIToolName                = attribute.Key("gen_ai.tool.name")
	AttrGenAIToolCallID              = attribute.Key("gen_ai.tool.call.id")
)

type genAIContextKey struct{}

// GenAIContext carries the stable agent identifiers needed to correlate model,
// tool, and nested-agent spans across traces.
type GenAIContext struct {
	ConversationID string
	AgentID        string
	AgentName      string
}

// WithGenAIContext returns ctx with complete GenAI correlation metadata
// attached. Missing fields are programming errors because GenAI operation spans
// require a stable conversation and agent identity.
func WithGenAIContext(ctx context.Context, meta GenAIContext) context.Context {
	validateGenAIContext(meta)
	return context.WithValue(ctx, genAIContextKey{}, meta)
}

// GenAIContextFromContext returns the GenAI correlation metadata on ctx.
func GenAIContextFromContext(ctx context.Context) (GenAIContext, bool) {
	meta, ok := ctx.Value(genAIContextKey{}).(GenAIContext)
	return meta, ok
}

// GenAIIdentityAttrs returns the standard conversation and agent attributes on
// ctx. Callers must attach GenAIContext before constructing operation spans.
func GenAIIdentityAttrs(ctx context.Context) []attribute.KeyValue {
	meta, ok := GenAIContextFromContext(ctx)
	if !ok {
		panic("telemetry: GenAI context is required")
	}
	return GenAIIdentityAttrsFor(meta)
}

// GenAIIdentityAttrsFor returns the standard conversation and agent attributes
// represented by meta.
func GenAIIdentityAttrsFor(meta GenAIContext) []attribute.KeyValue {
	validateGenAIContext(meta)
	return []attribute.KeyValue{
		AttrGenAIConversationID.String(meta.ConversationID),
		AttrGenAIAgentID.String(meta.AgentID),
		AttrGenAIAgentName.String(meta.AgentName),
	}
}

// GenAIOperationAttrs combines the required operation name with conversation
// and agent identity.
func GenAIOperationAttrs(ctx context.Context, operation string) []attribute.KeyValue {
	if operation == "" {
		panic("telemetry: GenAI operation name is required")
	}
	attrs := GenAIIdentityAttrs(ctx)
	attrs = append(attrs, AttrGenAIOperationName.String(operation))
	return attrs
}

// GenAIUsageAttrs returns token usage attributes for a completed model call.
func GenAIUsageAttrs(input, output, cacheRead, cacheCreation int) []attribute.KeyValue {
	return []attribute.KeyValue{
		AttrGenAIUsageInputTokens.Int(input),
		AttrGenAIUsageOutputTokens.Int(output),
		AttrGenAIUsageCacheReadTokens.Int(cacheRead),
		AttrGenAIUsageCacheCreationToken.Int(cacheCreation),
	}
}

// GenAIToolAttrs returns standard attributes for a tool execution span.
func GenAIToolAttrs(ctx context.Context, toolName, callID string) []attribute.KeyValue {
	if toolName == "" || callID == "" {
		panic("telemetry: GenAI tool attributes require tool name and call ID")
	}
	attrs := GenAIOperationAttrs(ctx, GenAIOperationExecuteTool)
	attrs = append(attrs,
		AttrGenAIToolName.String(toolName),
		AttrGenAIToolCallID.String(callID),
	)
	return attrs
}

func validateGenAIContext(meta GenAIContext) {
	if meta.ConversationID == "" || meta.AgentID == "" || meta.AgentName == "" {
		panic("telemetry: GenAI context requires conversation ID, agent ID, and agent name")
	}
}
