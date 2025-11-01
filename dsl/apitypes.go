package dsl

import (
	apitypesdesign "goa.design/goa-ai/apitypes/design"
)

// API type definitions for agent-related endpoints. These types are re-exported
// from apitypes/design for use in DSL definitions. They provide the standard
// types for agent run inputs, outputs, streaming chunks, and tool execution
// metadata.
//
// Use these types when defining service methods that interact with agents:
//   - AgentRunPayload: Input payload for agent run endpoints
//   - AgentRunResult: Terminal output from agent run endpoints
//   - AgentRunChunk: Streaming chunks for agent run progress
//   - AgentMessage: Individual messages in agent conversations
//   - AgentToolEvent: Tool execution outcomes
//   - AgentToolError: Structured tool error chains
//   - AgentRetryHint: Guidance for retrying failed tool calls
//   - AgentToolTelemetry: Telemetry metadata from tool execution
//   - AgentPlannerAnnotation: Planner observations and notes
//
// Example usage:
//
//	Service("orchestrator", func() {
//	    Agent("chat", "Chat orchestrator", func() {
//	        Uses(func() {
//	            UseMCPToolset("assistant", "assistant-mcp")
//	        })
//	    })
//
//	    Method("run", func() {
//	        Description("Invoke the chat agent")
//	        Payload(AgentRunPayload)
//	        StreamingResult(AgentRunChunk)
//	    })
//	})

var (
	// AgentRunPayload is the standard payload type for agent run endpoints.
	// It contains the conversation history, session/run identifiers, and optional
	// metadata. Use this as the Payload type for methods that invoke agents.
	//
	// Example:
	//   Method("run", func() {
	//       Payload(AgentRunPayload)
	//   })
	AgentRunPayload = apitypesdesign.AgentRunPayload

	// AgentRunResult is the standard result type for non-streaming agent run
	// endpoints. It contains the final assistant response, tool events, and
	// planner annotations from the completed run. Use this as the Result type
	// for methods that return terminal agent outputs.
	//
	// Example:
	//   Method("run", func() {
	//       Payload(AgentRunPayload)
	//       Result(AgentRunResult)
	//   })
	AgentRunResult = apitypesdesign.AgentRunResult

	// AgentRunChunk is the standard streaming result type for agent run endpoints.
	// It represents incremental progress updates during agent execution, including
	// message fragments, tool call notifications, tool results, and status updates.
	// Use this as the StreamingResult type for methods that stream agent progress.
	//
	// Example:
	//   Method("run", func() {
	//       Payload(AgentRunPayload)
	//       StreamingResult(AgentRunChunk)
	//       JSONRPC(func() {
	//           ServerSentEvents(func() {})
	//       })
	//   })
	AgentRunChunk = apitypesdesign.AgentRunChunk

	// AgentMessage represents a single message in an agent conversation transcript.
	// It includes the role (user, assistant, tool, system), content, and optional
	// metadata. This type is typically embedded within AgentRunPayload messages
	// arrays and AgentRunResult final fields.
	//
	// Example:
	//   Method("chat", func() {
	//       Payload(func() {
	//           Attribute("messages", ArrayOf(AgentMessage))
	//       })
	//   })
	AgentMessage = apitypesdesign.AgentMessage

	// AgentToolEvent represents the outcome of a tool call during agent execution.
	// It includes the tool name, result payload (on success), error information,
	// retry hints, and telemetry. This type is typically embedded within
	// AgentRunResult tool_events arrays.
	//
	// Example:
	//   Method("execute", func() {
	//       Result(func() {
	//           Attribute("tool_events", ArrayOf(AgentToolEvent))
	//       })
	//   })
	AgentToolEvent = apitypesdesign.AgentToolEvent

	// AgentToolError represents a structured error chain from tool execution failures.
	// It includes a human-readable message and optional nested cause. This type is
	// typically embedded within AgentToolEvent error fields.
	//
	// Example:
	//   Method("tools", func() {
	//       Result(func() {
	//           Attribute("error", AgentToolError)
	//       })
	//   })
	AgentToolError = apitypesdesign.AgentToolError

	// AgentRetryHint provides structured guidance after tool failures to help the
	// planner adjust behavior. It includes the failure reason, tool identifier,
	// missing fields, example inputs, and clarifying questions. This type is
	// typically embedded within AgentToolEvent retry_hint fields.
	//
	// Example:
	//   Method("tool_result", func() {
	//       Result(func() {
	//           Attribute("retry_hint", AgentRetryHint)
	//       })
	//   })
	AgentRetryHint = apitypesdesign.AgentRetryHint

	// AgentToolTelemetry captures telemetry metadata gathered during tool execution,
	// including duration, token usage, model identifier, and tool-specific metrics.
	// This type is typically embedded within AgentToolEvent telemetry fields.
	//
	// Example:
	//   Method("execute", func() {
	//       Result(func() {
	//           Attribute("telemetry", AgentToolTelemetry)
	//       })
	//   })
	AgentToolTelemetry = apitypesdesign.AgentToolTelemetry

	// AgentPlannerAnnotation represents optional notes or reasoning steps emitted
	// by planners during agent execution. It includes annotation text and optional
	// structured labels. This type is typically embedded within AgentRunResult
	// notes arrays.
	//
	// Example:
	//   Method("run", func() {
	//       Result(func() {
	//           Attribute("notes", ArrayOf(AgentPlannerAnnotation))
	//       })
	//   })
	AgentPlannerAnnotation = apitypesdesign.AgentPlannerAnnotation

	// AgentToolCallChunk represents a streaming notification about a scheduled tool
	// call. It includes the call identifier, tool name, and payload. This type is
	// typically embedded within AgentRunChunk tool_call fields.
	//
	// Example:
	//   Method("stream", func() {
	//       StreamingResult(func() {
	//           Attribute("tool_call", AgentToolCallChunk)
	//       })
	//   })
	AgentToolCallChunk = apitypesdesign.AgentToolCallChunk

	// AgentToolResultChunk represents a streaming notification containing the result
	// of a completed tool call. It includes the call identifier, result payload,
	// and optional error information. This type is typically embedded within
	// AgentRunChunk tool_result fields.
	//
	// Example:
	//   Method("stream", func() {
	//       StreamingResult(func() {
	//           Attribute("tool_result", AgentToolResultChunk)
	//       })
	//   })
	AgentToolResultChunk = apitypesdesign.AgentToolResultChunk

	// AgentRunStatusChunk represents a streaming notification about run status
	// changes (started, paused, resumed, completed). It includes the state and
	// optional status message. This type is typically embedded within AgentRunChunk
	// status fields.
	//
	// Example:
	//   Method("stream", func() {
	//       StreamingResult(func() {
	//           Attribute("status", AgentRunStatusChunk)
	//       })
	//   })
	AgentRunStatusChunk = apitypesdesign.AgentRunStatusChunk
)
