# Requirements Document

## Introduction

This specification addresses the architectural debt in both A2A (Agent-to-Agent) and MCP (Model Context Protocol) code generation within goa-ai. The current implementations were developed with some duplication and inconsistent abstractions. As the author of goa-ai, I recognize that elegant, conceptually correct code requires unified abstractions that capture the essential structure shared across protocol implementations.

The goal is to:
1. Extract shared infrastructure between A2A and MCP into reusable components
2. Align A2A's architecture with MCP's proven patterns while improving MCP itself
3. Eliminate code duplication while preserving protocol-specific semantics
4. Replace brittle string-based patching with proper template composition
5. Leverage Goa's JSON-RPC codegen instead of handwritten client code
6. Add missing features to A2A that MCP already provides (retry, policy injection, registration)
7. Improve MCP's patching mechanisms to be more robust and maintainable

## Glossary

- **A2A**: Agent-to-Agent protocol - Google's protocol for agent interoperability
- **MCP**: Model Context Protocol - Anthropic's protocol for tool/resource exposure
- **Expression Builder**: Component that constructs Goa expression trees for protocol services
- **Adapter**: Generated code that bridges protocol methods to underlying service implementations
- **Agent Card**: A2A's discovery document describing agent capabilities and skills
- **Skill**: A2A's term for a discrete capability (analogous to MCP's Tool)
- **Protocol Service**: A synthetic Goa service generated to implement protocol methods
- **JSON-RPC**: The transport protocol used by both A2A and MCP
- **SSE**: Server-Sent Events - streaming mechanism for real-time updates

## Requirements

### Requirement 1

**User Story:** As a goa-ai maintainer, I want shared expression builder infrastructure, so that A2A and MCP can reuse common Goa expression construction patterns without duplication.

#### Acceptance Criteria

1. WHEN building protocol expressions THEN the system SHALL provide a shared base type containing common methods (PrepareAndValidate, collectUserTypes, getOrCreateType, userTypeAttr)
2. WHEN the A2A expression builder is instantiated THEN the system SHALL embed or delegate to the shared base type for common operations
3. WHEN the MCP expression builder is instantiated THEN the system SHALL embed or delegate to the shared base type for common operations
4. WHEN building HTTP service expressions THEN the system SHALL provide a shared helper that configures JSON-RPC routes and SSE endpoints
5. WHEN collecting user types for code generation THEN the system SHALL use a single deterministic sorting implementation shared across protocols

### Requirement 2

**User Story:** As a goa-ai maintainer, I want A2A to use a proper adapter generator pattern, so that type references, payload handling, and code generation follow the same robust architecture as MCP.

#### Acceptance Criteria

1. WHEN generating A2A adapter code THEN the system SHALL use a dedicated a2aAdapterGenerator struct mirroring MCP's adapterGenerator
2. WHEN resolving type references in A2A adapters THEN the system SHALL use Goa's NameScope helpers (GoFullTypeRef) instead of string concatenation
3. WHEN building adapter data THEN the system SHALL compute skill metadata, security schemes, and type references through the adapter generator
4. WHEN the adapter generator builds skill data THEN the system SHALL derive input schemas from exported tool payloads using the same JSON schema generation as MCP
5. WHEN generating A2A adapter files THEN the system SHALL follow MCP's pattern of composing multiple template sections (core, tools, security)

### Requirement 3

**User Story:** As a goa-ai maintainer, I want A2A client generation to leverage Goa's JSON-RPC codegen, so that the client benefits from battle-tested code generation rather than handwritten templates.

#### Acceptance Criteria

1. WHEN generating A2A client code THEN the system SHALL use Goa's jsonrpccodegen.ClientFiles instead of handwritten template code
2. WHEN the A2A client needs protocol-specific behavior THEN the system SHALL patch generated files using the same pattern as patchMCPJSONRPCClientFiles
3. WHEN streaming responses are needed THEN the system SHALL configure SSE through Goa's standard streaming mechanisms
4. WHEN authentication is required THEN the system SHALL generate auth helpers that integrate with Goa's security infrastructure
5. WHEN the generated client encounters errors THEN the system SHALL provide structured error types consistent with JSON-RPC error codes

### Requirement 4

**User Story:** As a goa-ai maintainer, I want A2A types defined in a single source of truth, so that Goa expressions, generated structs, and template code all derive from the same definitions.

#### Acceptance Criteria

1. WHEN A2A protocol types are needed THEN the system SHALL generate them from Goa expressions in a2a_types.go
2. WHEN templates reference A2A types THEN the system SHALL import generated types from the protocol service package
3. WHEN the agent card template needs type definitions THEN the system SHALL reference types from the generated A2A service package
4. WHEN the A2A client template needs type definitions THEN the system SHALL reference types from the generated A2A service package
5. WHEN type definitions change THEN the system SHALL require updates only in a2a_types.go expression builders

### Requirement 5

**User Story:** As a goa-ai maintainer, I want A2A to support retry mechanisms, so that transient failures are handled gracefully like in MCP.

#### Acceptance Criteria

1. WHEN an A2A client call fails with a retryable error THEN the system SHALL retry the request using exponential backoff
2. WHEN configuring retry behavior THEN the system SHALL accept retry options (max attempts, backoff strategy) consistent with MCP's retry package
3. WHEN a streaming call is interrupted THEN the system SHALL provide reconnection support with proper state recovery
4. WHEN retry is exhausted THEN the system SHALL return a structured error indicating all attempts failed

### Requirement 6

**User Story:** As a goa-ai maintainer, I want A2A to support policy injection via headers, so that skill filtering can be controlled at the transport layer like MCP's tool filtering.

#### Acceptance Criteria

1. WHEN processing A2A requests THEN the system SHALL extract policy headers (x-a2a-allow-skills, x-a2a-deny-skills) from HTTP requests
2. WHEN policy headers are present THEN the system SHALL inject them into the request context for downstream consumption
3. WHEN listing skills THEN the system SHALL filter results based on allow/deny policies in context
4. WHEN executing tasks THEN the system SHALL validate requested skills against policy constraints

### Requirement 7

**User Story:** As a goa-ai maintainer, I want A2A to generate runtime registration helpers, so that agents can be registered with toolset registries like MCP services.

#### Acceptance Criteria

1. WHEN an agent has exported toolsets THEN the system SHALL generate a registration helper file
2. WHEN the registration helper is called THEN the system SHALL register all exported skills with their schemas and metadata
3. WHEN skill schemas are registered THEN the system SHALL include input/output schemas derived from tool payloads
4. WHEN the registration helper is generated THEN the system SHALL follow the same pattern as MCP's registerFile

### Requirement 8

**User Story:** As a goa-ai maintainer, I want security scheme handling consolidated, so that A2A security configuration is defined once and used consistently across card, adapter, and client generation.

#### Acceptance Criteria

1. WHEN building security scheme data THEN the system SHALL use a single buildA2ASecurityData function called from one location
2. WHEN the adapter needs security schemes THEN the system SHALL receive them through the adapter data structure
3. WHEN the card needs security schemes THEN the system SHALL receive them through the card data structure
4. WHEN the client needs auth providers THEN the system SHALL generate them based on the same security scheme definitions
5. WHEN OAuth2 flows are configured THEN the system SHALL support client_credentials and authorization_code flows consistently

### Requirement 9

**User Story:** As a goa-ai maintainer, I want the A2A adapter template to be modular, so that different concerns (core, tasks, card, security) are in separate template sections.

#### Acceptance Criteria

1. WHEN generating the A2A adapter THEN the system SHALL compose multiple template sections rather than one monolithic template
2. WHEN the adapter handles task operations THEN the system SHALL use a dedicated "a2a-adapter-tasks" template section
3. WHEN the adapter handles agent card THEN the system SHALL use a dedicated "a2a-adapter-card" template section
4. WHEN the adapter handles security THEN the system SHALL use a dedicated "a2a-adapter-security" template section
5. WHEN adding new A2A features THEN the system SHALL allow adding new template sections without modifying existing ones

### Requirement 10

**User Story:** As a goa-ai developer, I want comprehensive tests for A2A code generation, so that refactoring does not break existing functionality.

#### Acceptance Criteria

1. WHEN shared infrastructure is extracted THEN the system SHALL include unit tests for the shared base type
2. WHEN A2A adapter generation changes THEN the system SHALL include golden file tests comparing generated output
3. WHEN A2A client generation changes THEN the system SHALL include integration tests verifying JSON-RPC communication
4. WHEN security scheme mapping changes THEN the system SHALL include property-based tests for scheme conversion
5. WHEN type generation changes THEN the system SHALL include round-trip tests for serialization/deserialization

### Requirement 11

**User Story:** As a goa-ai maintainer, I want MCP's string-based patching replaced with proper template composition, so that code generation is more robust and maintainable.

#### Acceptance Criteria

1. WHEN patching JSON-RPC client files THEN the system SHALL use template hooks or section overrides instead of strings.ReplaceAll
2. WHEN patching JSON-RPC server files THEN the system SHALL use template composition instead of string surgery
3. WHEN injecting context values THEN the system SHALL use a dedicated middleware or interceptor pattern
4. WHEN adding imports to generated files THEN the system SHALL use Goa's AddImport helper consistently
5. WHEN the underlying Goa templates change THEN the system SHALL fail fast with clear errors rather than silently producing incorrect code

### Requirement 12

**User Story:** As a goa-ai maintainer, I want shared utility functions for import path construction and attribute import gathering, so that both protocols use consistent, tested implementations.

#### Acceptance Criteria

1. WHEN constructing import paths THEN the system SHALL use a shared joinImportPath function in a common package
2. WHEN gathering attribute imports THEN the system SHALL use a shared gatherAttributeImports function
3. WHEN the shared utilities are used THEN the system SHALL not have protocol-specific copies (no joinImportPathMCP, joinImportPathA2A)
4. WHEN external user types are referenced THEN the system SHALL resolve their import paths consistently across protocols

### Requirement 13

**User Story:** As a goa-ai maintainer, I want a protocol configuration abstraction, so that protocol-specific settings (path, version, capabilities) are encapsulated cleanly.

#### Acceptance Criteria

1. WHEN configuring a protocol service THEN the system SHALL use a ProtocolConfig interface or struct
2. WHEN the protocol config specifies a JSON-RPC path THEN the system SHALL use it for route configuration
3. WHEN the protocol config specifies capabilities THEN the system SHALL include them in initialization responses
4. WHEN adding a new protocol THEN the system SHALL only need to implement the protocol config interface and protocol-specific types/methods
