// Package codegen contains generators and templates used to produce MCP service
// adapters, transports, and client wrappers on top of Goa-generated code.
//
// # Patching Patterns
//
// This package uses string-based patching to customize Goa-generated JSON-RPC
// code for MCP-specific requirements. The patching functions modify generated
// code in-memory before it is written to disk.
//
// ## Why Patching?
//
// Goa's JSON-RPC codegen produces generic client/server code. MCP requires
// specific customizations that cannot be achieved through Goa's extension points:
//
//   - SSE Accept headers for streaming endpoints
//   - Context propagation through SSE encoders
//   - Structured JSON-RPC error types
//   - Policy injection from HTTP headers
//   - Retry support with tool-specific wrappers
//
// ## Patching Functions
//
// The following functions perform code patching:
//
//   - patchMCPJSONRPCClientFiles: Patches client code for SSE headers, retry
//     support, and structured errors
//   - patchMCPJSONRPCServerSSEFiles: Patches SSE server streams for context
//     propagation
//   - patchMCPJSONRPCServerBaseFiles: Patches base server for policy injection
//   - patchGeneratedCLISupportToUseMCPAdapter: Patches CLI to use MCP adapter
//
// ## Pattern Validation
//
// All patching functions use the shared.ReplaceOnce and shared.ReplaceAll
// helpers which validate that expected patterns exist before replacing. If a
// pattern is not found, a warning is logged. This ensures that if Goa's
// templates change, we fail fast with clear errors rather than silently
// producing incorrect code.
//
// Required patches use shared.ReplaceOnce/ReplaceAll and log warnings on failure.
// Optional patches use shared.ReplaceOnceOptional/ReplaceAllOptional and fail
// silently (for patterns that may not exist in all code paths).
//
// ## Maintenance Guidelines
//
// When updating patching code:
//
//  1. Always use the shared patch utilities for pattern validation
//  2. Provide descriptive context strings for error messages
//  3. Mark patches as optional only if the pattern may legitimately not exist
//  4. Run integration tests after Goa version upgrades to catch template changes
//  5. Document the purpose of each patch in comments
//
// ## Future Improvements
//
// The long-term goal is to replace string-based patching with:
//
//   - Template hooks or section overrides in Goa
//   - Middleware/interceptor patterns for context injection
//   - Goa's AddImport helper for import manipulation
//
// ## Middleware Pattern Evaluation
//
// Context injection (e.g., policy headers) could potentially use HTTP middleware
// instead of string patching. However, the current approach was chosen because:
//
//  1. Injection must happen after JSON-RPC parsing but before method dispatch,
//     which is inside the Server.processRequest method.
//  2. HTTP middleware operates at the mux level, before JSON-RPC parsing,
//     which is too early for some use cases.
//  3. The current approach is self-contained and doesn't require user
//     coordination for middleware setup.
//
// A better long-term solution would be for Goa's JSON-RPC codegen to provide
// a hook point or interceptor interface that allows injecting code at specific
// points in the request lifecycle without string patching.
package codegen
