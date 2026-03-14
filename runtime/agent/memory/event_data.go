// Package memory persists event payloads as generic maps so stores can serialize
// them uniformly, but runtime and transcript code should interact with the typed
// payload structs in this file. Each event kind owns its map encoding here so the
// rest of the runtime never reaches into stringly-typed event data.
package memory

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// EventData describes a typed persisted event payload that can marshal itself
	// into the generic map form stored by memory backends.
	EventData interface {
		// EventType reports which persisted event kind this payload belongs to.
		EventType() EventType
		// ToMap converts the typed payload into the generic storage representation.
		ToMap() map[string]any
	}

	// UserMessageData stores a durable end-user message payload when runtimes
	// choose to persist user utterances.
	UserMessageData struct {
		// Message is the user-visible text content.
		Message string
		// Structured carries optional typed user payloads.
		Structured any
	}

	// AssistantMessageData stores a final assistant response payload.
	AssistantMessageData struct {
		// Message is the assistant text content.
		Message string
		// Structured carries optional typed assistant output.
		Structured any
	}

	// ToolCallData stores the persisted form of a scheduled tool invocation.
	ToolCallData struct {
		// ToolCallID uniquely identifies the invocation.
		ToolCallID string
		// ParentToolCallID links nested tool calls to their parent.
		ParentToolCallID string
		// ToolName is the canonical tool identifier.
		ToolName tools.Ident
		// PayloadJSON is the canonical JSON encoding of the tool input.
		PayloadJSON rawjson.Message
		// Queue is the activity queue where execution was scheduled.
		Queue string
		// ExpectedChildrenTotal records the expected number of child calls.
		ExpectedChildrenTotal int
	}

	// ToolResultData stores the persisted form of a completed tool invocation.
	ToolResultData struct {
		// ToolCallID uniquely identifies the invocation.
		ToolCallID string
		// ParentToolCallID links nested tool calls to their parent.
		ParentToolCallID string
		// ToolName is the canonical tool identifier.
		ToolName tools.Ident
		// ResultJSON is the canonical JSON encoding of the successful result.
		ResultJSON rawjson.Message
		// Preview is the already-rendered user-facing result summary.
		Preview string
		// Bounds describes bounded-result metadata when present.
		Bounds *agent.Bounds
		// Duration is the wall-clock tool execution time.
		Duration time.Duration
		// ErrorMessage is the plain-text tool failure message.
		ErrorMessage string
	}

	// PlannerNoteData stores planner-generated annotations.
	PlannerNoteData struct {
		// Note is the planner annotation text.
		Note string
	}

	// ThinkingData stores provider-issued reasoning blocks for exact replay.
	ThinkingData struct {
		// Text is the plaintext reasoning content.
		Text string
		// Signature is the provider-issued signature for plaintext reasoning.
		Signature string
		// Redacted holds opaque redacted reasoning bytes.
		Redacted []byte
		// ContentIndex is the provider content block index.
		ContentIndex int
		// Final reports whether the provider finalized this block.
		Final bool
	}
)

const (
	eventFieldMessage               = "message"
	eventFieldStructured            = "structured"
	eventFieldToolCallID            = "tool_call_id"
	eventFieldParentToolCallID      = "parent_tool_call_id"
	eventFieldToolName              = "tool_name"
	eventFieldPayload               = "payload"
	eventFieldQueue                 = "queue"
	eventFieldExpectedChildrenTotal = "expected_children_total"
	eventFieldResultJSON            = "result_json"
	eventFieldPreview               = "preview"
	eventFieldBounds                = "bounds"
	eventFieldDuration              = "duration"
	eventFieldErrorMessage          = "error_message"
	eventFieldNote                  = "note"
	eventFieldText                  = "text"
	eventFieldSignature             = "signature"
	eventFieldRedacted              = "redacted"
	eventFieldContentIndex          = "content_index"
	eventFieldFinal                 = "final"
)

// NewEvent converts typed event data into the persisted Event form.
func NewEvent(timestamp time.Time, data EventData, labels map[string]string) Event {
	return Event{
		Type:      data.EventType(),
		Timestamp: timestamp,
		Data:      data.ToMap(),
		Labels:    cloneEventLabels(labels),
	}
}

// DecodeUserMessageData reconstructs typed user-message payload data.
func DecodeUserMessageData(event Event) (UserMessageData, error) {
	var data UserMessageData
	if event.Type != EventUserMessage {
		return data, fmt.Errorf("memory: decode %s as %s", event.Type, EventUserMessage)
	}
	return data, data.FromMap(event.Data)
}

// DecodeAssistantMessageData reconstructs typed assistant-message payload data.
func DecodeAssistantMessageData(event Event) (AssistantMessageData, error) {
	var data AssistantMessageData
	if event.Type != EventAssistantMessage {
		return data, fmt.Errorf("memory: decode %s as %s", event.Type, EventAssistantMessage)
	}
	return data, data.FromMap(event.Data)
}

// DecodeToolCallData reconstructs typed tool-call payload data.
func DecodeToolCallData(event Event) (ToolCallData, error) {
	var data ToolCallData
	if event.Type != EventToolCall {
		return data, fmt.Errorf("memory: decode %s as %s", event.Type, EventToolCall)
	}
	return data, data.FromMap(event.Data)
}

// DecodeToolResultData reconstructs typed tool-result payload data.
func DecodeToolResultData(event Event) (ToolResultData, error) {
	var data ToolResultData
	if event.Type != EventToolResult {
		return data, fmt.Errorf("memory: decode %s as %s", event.Type, EventToolResult)
	}
	return data, data.FromMap(event.Data)
}

// DecodePlannerNoteData reconstructs typed planner-note payload data.
func DecodePlannerNoteData(event Event) (PlannerNoteData, error) {
	var data PlannerNoteData
	if event.Type != EventPlannerNote {
		return data, fmt.Errorf("memory: decode %s as %s", event.Type, EventPlannerNote)
	}
	return data, data.FromMap(event.Data)
}

// DecodeThinkingData reconstructs typed thinking-block payload data.
func DecodeThinkingData(event Event) (ThinkingData, error) {
	var data ThinkingData
	if event.Type != EventThinking {
		return data, fmt.Errorf("memory: decode %s as %s", event.Type, EventThinking)
	}
	return data, data.FromMap(event.Data)
}

// EventType reports the persisted event kind for a user message.
func (UserMessageData) EventType() EventType {
	return EventUserMessage
}

// ToMap converts the user message payload into its persisted map shape.
func (d UserMessageData) ToMap() map[string]any {
	return messageDataMap(d.Message, d.Structured)
}

// FromMap reconstructs typed user-message data from the persisted map shape.
func (d *UserMessageData) FromMap(data any) error {
	if typed, ok := data.(UserMessageData); ok {
		*d = typed
		return nil
	}
	if typed, ok := data.(*UserMessageData); ok {
		if typed == nil {
			return fmt.Errorf("memory: %s data is nil", EventUserMessage)
		}
		*d = *typed
		return nil
	}
	m, err := requireEventMap(EventUserMessage, data)
	if err != nil {
		return err
	}
	d.Message, err = optionalStringField(EventUserMessage, m, eventFieldMessage)
	if err != nil {
		return err
	}
	d.Structured = m[eventFieldStructured]
	return nil
}

// EventType reports the persisted event kind for an assistant message.
func (AssistantMessageData) EventType() EventType {
	return EventAssistantMessage
}

// ToMap converts the assistant message payload into its persisted map shape.
func (d AssistantMessageData) ToMap() map[string]any {
	return messageDataMap(d.Message, d.Structured)
}

// FromMap reconstructs typed assistant-message data from the persisted map shape.
func (d *AssistantMessageData) FromMap(data any) error {
	if typed, ok := data.(AssistantMessageData); ok {
		*d = typed
		return nil
	}
	if typed, ok := data.(*AssistantMessageData); ok {
		if typed == nil {
			return fmt.Errorf("memory: %s data is nil", EventAssistantMessage)
		}
		*d = *typed
		return nil
	}
	m, err := requireEventMap(EventAssistantMessage, data)
	if err != nil {
		return err
	}
	d.Message, err = optionalStringField(EventAssistantMessage, m, eventFieldMessage)
	if err != nil {
		return err
	}
	d.Structured = m[eventFieldStructured]
	return nil
}

// EventType reports the persisted event kind for a tool call.
func (ToolCallData) EventType() EventType {
	return EventToolCall
}

// ToMap converts the tool call payload into its persisted map shape.
func (d ToolCallData) ToMap() map[string]any {
	m := map[string]any{
		eventFieldToolCallID: d.ToolCallID,
		eventFieldToolName:   string(d.ToolName),
	}
	if d.ParentToolCallID != "" {
		m[eventFieldParentToolCallID] = d.ParentToolCallID
	}
	if len(d.PayloadJSON) > 0 {
		m[eventFieldPayload] = string(d.PayloadJSON)
	}
	if d.Queue != "" {
		m[eventFieldQueue] = d.Queue
	}
	if d.ExpectedChildrenTotal != 0 {
		m[eventFieldExpectedChildrenTotal] = d.ExpectedChildrenTotal
	}
	return m
}

// FromMap reconstructs typed tool-call data from the persisted map shape.
func (d *ToolCallData) FromMap(data any) error {
	if typed, ok := data.(ToolCallData); ok {
		*d = cloneToolCallData(typed)
		return nil
	}
	if typed, ok := data.(*ToolCallData); ok {
		if typed == nil {
			return fmt.Errorf("memory: %s data is nil", EventToolCall)
		}
		*d = cloneToolCallData(*typed)
		return nil
	}
	m, err := requireEventMap(EventToolCall, data)
	if err != nil {
		return err
	}
	d.ToolCallID, err = requiredStringField(EventToolCall, m, eventFieldToolCallID)
	if err != nil {
		return err
	}
	toolName, err := requiredStringField(EventToolCall, m, eventFieldToolName)
	if err != nil {
		return err
	}
	d.ToolName = tools.Ident(toolName)
	d.ParentToolCallID, err = optionalStringField(EventToolCall, m, eventFieldParentToolCallID)
	if err != nil {
		return err
	}
	d.PayloadJSON, err = optionalRawJSONField(EventToolCall, m, eventFieldPayload)
	if err != nil {
		return err
	}
	d.Queue, err = optionalStringField(EventToolCall, m, eventFieldQueue)
	if err != nil {
		return err
	}
	d.ExpectedChildrenTotal, err = optionalIntField(EventToolCall, m, eventFieldExpectedChildrenTotal)
	if err != nil {
		return err
	}
	return nil
}

// Input decodes the canonical tool-call payload into a plain JSON-compatible value.
func (d ToolCallData) Input() (any, error) {
	return decodeCanonicalJSONValue(EventToolCall, d.PayloadJSON, eventFieldPayload)
}

// EventType reports the persisted event kind for a tool result.
func (ToolResultData) EventType() EventType {
	return EventToolResult
}

// ToMap converts the tool result payload into its persisted map shape.
func (d ToolResultData) ToMap() map[string]any {
	m := map[string]any{
		eventFieldToolCallID: d.ToolCallID,
		eventFieldToolName:   string(d.ToolName),
	}
	if d.ParentToolCallID != "" {
		m[eventFieldParentToolCallID] = d.ParentToolCallID
	}
	if len(d.ResultJSON) > 0 {
		m[eventFieldResultJSON] = string(d.ResultJSON)
	}
	if d.Preview != "" {
		m[eventFieldPreview] = d.Preview
	}
	if d.Bounds != nil {
		m[eventFieldBounds] = cloneBounds(d.Bounds)
	}
	if d.Duration != 0 {
		m[eventFieldDuration] = d.Duration
	}
	if d.ErrorMessage != "" {
		m[eventFieldErrorMessage] = d.ErrorMessage
	}
	return m
}

// FromMap reconstructs typed tool-result data from the persisted map shape.
func (d *ToolResultData) FromMap(data any) error {
	if typed, ok := data.(ToolResultData); ok {
		*d = cloneToolResultData(typed)
		return nil
	}
	if typed, ok := data.(*ToolResultData); ok {
		if typed == nil {
			return fmt.Errorf("memory: %s data is nil", EventToolResult)
		}
		*d = cloneToolResultData(*typed)
		return nil
	}
	m, err := requireEventMap(EventToolResult, data)
	if err != nil {
		return err
	}
	d.ToolCallID, err = requiredStringField(EventToolResult, m, eventFieldToolCallID)
	if err != nil {
		return err
	}
	toolName, err := requiredStringField(EventToolResult, m, eventFieldToolName)
	if err != nil {
		return err
	}
	d.ToolName = tools.Ident(toolName)
	d.ParentToolCallID, err = optionalStringField(EventToolResult, m, eventFieldParentToolCallID)
	if err != nil {
		return err
	}
	d.ResultJSON, err = optionalRawJSONField(EventToolResult, m, eventFieldResultJSON)
	if err != nil {
		return err
	}
	d.Preview, err = optionalStringField(EventToolResult, m, eventFieldPreview)
	if err != nil {
		return err
	}
	d.Bounds, err = optionalBoundsField(EventToolResult, m, eventFieldBounds)
	if err != nil {
		return err
	}
	d.Duration, err = optionalDurationField(EventToolResult, m, eventFieldDuration)
	if err != nil {
		return err
	}
	d.ErrorMessage, err = optionalStringField(EventToolResult, m, eventFieldErrorMessage)
	if err != nil {
		return err
	}
	return nil
}

// EventType reports the persisted event kind for a planner note.
func (PlannerNoteData) EventType() EventType {
	return EventPlannerNote
}

// ToMap converts the planner note payload into its persisted map shape.
func (d PlannerNoteData) ToMap() map[string]any {
	return map[string]any{
		eventFieldNote: d.Note,
	}
}

// FromMap reconstructs typed planner-note data from the persisted map shape.
func (d *PlannerNoteData) FromMap(data any) error {
	if typed, ok := data.(PlannerNoteData); ok {
		*d = typed
		return nil
	}
	if typed, ok := data.(*PlannerNoteData); ok {
		if typed == nil {
			return fmt.Errorf("memory: %s data is nil", EventPlannerNote)
		}
		*d = *typed
		return nil
	}
	m, err := requireEventMap(EventPlannerNote, data)
	if err != nil {
		return err
	}
	d.Note, err = requiredStringField(EventPlannerNote, m, eventFieldNote)
	return err
}

// EventType reports the persisted event kind for a thinking block.
func (ThinkingData) EventType() EventType {
	return EventThinking
}

// ToMap converts the thinking payload into its persisted map shape.
func (d ThinkingData) ToMap() map[string]any {
	m := map[string]any{
		eventFieldContentIndex: d.ContentIndex,
		eventFieldFinal:        d.Final,
	}
	if d.Text != "" {
		m[eventFieldText] = d.Text
	}
	if d.Signature != "" {
		m[eventFieldSignature] = d.Signature
	}
	if len(d.Redacted) > 0 {
		m[eventFieldRedacted] = base64.StdEncoding.EncodeToString(d.Redacted)
	}
	return m
}

// FromMap reconstructs typed thinking data from the persisted map shape.
func (d *ThinkingData) FromMap(data any) error {
	if typed, ok := data.(ThinkingData); ok {
		*d = cloneThinkingData(typed)
		return nil
	}
	if typed, ok := data.(*ThinkingData); ok {
		if typed == nil {
			return fmt.Errorf("memory: %s data is nil", EventThinking)
		}
		*d = cloneThinkingData(*typed)
		return nil
	}
	m, err := requireEventMap(EventThinking, data)
	if err != nil {
		return err
	}
	d.Text, err = optionalStringField(EventThinking, m, eventFieldText)
	if err != nil {
		return err
	}
	d.Signature, err = optionalStringField(EventThinking, m, eventFieldSignature)
	if err != nil {
		return err
	}
	d.Redacted, err = optionalBytesField(EventThinking, m, eventFieldRedacted)
	if err != nil {
		return err
	}
	d.ContentIndex, err = optionalIntField(EventThinking, m, eventFieldContentIndex)
	if err != nil {
		return err
	}
	d.Final, err = optionalBoolField(EventThinking, m, eventFieldFinal)
	if err != nil {
		return err
	}
	return nil
}

func messageDataMap(message string, structured any) map[string]any {
	m := map[string]any{}
	if message != "" {
		m[eventFieldMessage] = message
	}
	if structured != nil {
		m[eventFieldStructured] = structured
	}
	return m
}

// requireEventMap validates the storage boundary and returns the map payload.
func requireEventMap(eventType EventType, data any) (map[string]any, error) {
	if data == nil {
		return nil, fmt.Errorf("memory: %s data is nil", eventType)
	}
	m, ok := data.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("memory: %s data must be map[string]any, got %T", eventType, data)
	}
	return m, nil
}

// requiredStringField loads a required string field from an event map.
func requiredStringField(eventType EventType, data map[string]any, key string) (string, error) {
	value, ok := data[key]
	if !ok {
		return "", fmt.Errorf("memory: %s missing %q", eventType, key)
	}
	return stringValue(eventType, key, value)
}

// optionalStringField loads an optional string field from an event map.
func optionalStringField(eventType EventType, data map[string]any, key string) (string, error) {
	value, ok := data[key]
	if !ok || value == nil {
		return "", nil
	}
	return stringValue(eventType, key, value)
}

// optionalRawJSONField loads an optional canonical JSON field from an event map.
func optionalRawJSONField(eventType EventType, data map[string]any, key string) (rawjson.Message, error) {
	value, ok := data[key]
	if !ok || value == nil {
		return nil, nil
	}
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return nil, nil
		}
		return rawjson.Message(typed), nil
	case rawjson.Message:
		if len(typed) == 0 {
			return nil, nil
		}
		return append(rawjson.Message(nil), typed...), nil
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("memory: encode %s field %q: %w", eventType, key, err)
		}
		if len(encoded) == 0 || bytes.Equal(encoded, []byte("null")) {
			return nil, nil
		}
		return rawjson.Message(encoded), nil
	}
}

// optionalBoundsField loads optional bounded-result metadata from an event map.
func optionalBoundsField(eventType EventType, data map[string]any, key string) (*agent.Bounds, error) {
	value, ok := data[key]
	if !ok || value == nil {
		return nil, nil
	}
	switch typed := value.(type) {
	case *agent.Bounds:
		return cloneBounds(typed), nil
	case agent.Bounds:
		bounds := typed
		return &bounds, nil
	default:
		var bounds agent.Bounds
		if err := decodeStructuredValue(value, &bounds); err != nil {
			return nil, fmt.Errorf("memory: decode %s field %q: %w", eventType, key, err)
		}
		return &bounds, nil
	}
}

// optionalDurationField loads an optional duration field from an event map.
func optionalDurationField(eventType EventType, data map[string]any, key string) (time.Duration, error) {
	value, ok := data[key]
	if !ok || value == nil {
		return 0, nil
	}
	switch typed := value.(type) {
	case time.Duration:
		return typed, nil
	case int:
		return time.Duration(typed), nil
	case int32:
		return time.Duration(typed), nil
	case int64:
		return time.Duration(typed), nil
	case float64:
		return time.Duration(typed), nil
	default:
		return 0, fmt.Errorf("memory: %s field %q must be duration-compatible, got %T", eventType, key, value)
	}
}

// optionalIntField loads an optional integer field from an event map.
func optionalIntField(eventType EventType, data map[string]any, key string) (int, error) {
	value, ok := data[key]
	if !ok || value == nil {
		return 0, nil
	}
	switch typed := value.(type) {
	case int:
		return typed, nil
	case int8:
		return int(typed), nil
	case int16:
		return int(typed), nil
	case int32:
		return int(typed), nil
	case int64:
		return int(typed), nil
	case float64:
		return int(typed), nil
	default:
		return 0, fmt.Errorf("memory: %s field %q must be int-compatible, got %T", eventType, key, value)
	}
}

// optionalBoolField loads an optional bool field from an event map.
func optionalBoolField(eventType EventType, data map[string]any, key string) (bool, error) {
	value, ok := data[key]
	if !ok || value == nil {
		return false, nil
	}
	typed, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("memory: %s field %q must be bool, got %T", eventType, key, value)
	}
	return typed, nil
}

// optionalBytesField loads an optional base64-encoded byte field from an event map.
func optionalBytesField(eventType EventType, data map[string]any, key string) ([]byte, error) {
	value, ok := data[key]
	if !ok || value == nil {
		return nil, nil
	}
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return nil, nil
		}
		decoded, err := base64.StdEncoding.DecodeString(typed)
		if err != nil {
			return nil, fmt.Errorf("memory: %s field %q must be base64, got invalid string", eventType, key)
		}
		return decoded, nil
	case []byte:
		if len(typed) == 0 {
			return nil, nil
		}
		return append([]byte(nil), typed...), nil
	default:
		return nil, fmt.Errorf("memory: %s field %q must be string, got %T", eventType, key, value)
	}
}

func stringValue(eventType EventType, key string, value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case tools.Ident:
		return string(typed), nil
	default:
		return "", fmt.Errorf("memory: %s field %q must be string, got %T", eventType, key, value)
	}
}

// decodeCanonicalJSONValue decodes canonical JSON text into plain Go values.
func decodeCanonicalJSONValue(eventType EventType, raw rawjson.Message, key string) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return nil, fmt.Errorf("memory: decode %s field %q: %w", eventType, key, err)
	}
	return value, nil
}

// decodeStructuredValue decodes arbitrary persisted map values into the target type.
func decodeStructuredValue(value any, target any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

func cloneEventLabels(labels map[string]string) map[string]string {
	if len(labels) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(labels))
	for key, value := range labels {
		cloned[key] = value
	}
	return cloned
}

func cloneBounds(bounds *agent.Bounds) *agent.Bounds {
	if bounds == nil {
		return nil
	}
	cloned := *bounds
	return &cloned
}

func cloneToolCallData(data ToolCallData) ToolCallData {
	data.PayloadJSON = append(rawjson.Message(nil), data.PayloadJSON...)
	return data
}

func cloneToolResultData(data ToolResultData) ToolResultData {
	data.ResultJSON = append(rawjson.Message(nil), data.ResultJSON...)
	data.Bounds = cloneBounds(data.Bounds)
	return data
}

func cloneThinkingData(data ThinkingData) ThinkingData {
	data.Redacted = append([]byte(nil), data.Redacted...)
	return data
}
