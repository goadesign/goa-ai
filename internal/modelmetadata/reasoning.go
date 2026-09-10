// Package modelmetadata names provider metadata shared by transcript operations
// and the adapters that produce and replay it. These keys are not application
// configuration or a provider registry.
package modelmetadata

// OpenAIReasoningItems contains the exact native Responses reasoning items.
// Preserve the serialized key so existing saved messages remain readable.
const OpenAIReasoningItems = "openai_reasoning_items"
