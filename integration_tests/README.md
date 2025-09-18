# MCP Plugin Integration Tests

Comprehensive integration tests for the Model Context Protocol (MCP) plugin using the Goa test engine.

## Overview

These tests validate the MCP plugin's implementation of the Model Context Protocol specification, ensuring correct behavior for all protocol features including tools, resources, prompts, and notifications.

## Quick Start

### Running All Tests

```bash
# From the integration_tests directory
go test ./tests

# With verbose output
go test -v ./tests

# With specific test
go test -v ./tests -run TestMCPProtocol
```

### Running Specific Test Categories

```bash
# Protocol compliance tests
go test -v ./tests -run TestMCPProtocol

# Tools functionality
go test -v ./tests -run TestMCPTools

# Resources functionality
go test -v ./tests -run TestMCPResources

# Prompts functionality
go test -v ./tests -run TestMCPPrompts
```

### Environment Variables

Configure test execution with these environment variables:

```bash
# Run tests in parallel
TEST_PARALLEL=true go test ./tests

# Filter specific scenarios
TEST_FILTER="initialize.*" go test ./tests

# Keep generated code for debugging
TEST_KEEP_GENERATED=true go test ./tests

# Enable debug output
TEST_DEBUG=true go test ./tests

# Custom timeout
TEST_TIMEOUT=60s go test ./tests

# Use external server
TEST_SERVER_URL=http://localhost:8080 go test ./tests
```

## Test Structure

```
integration_tests/
├── README.md                 # This file
├── framework/               # MCP test framework
│   └── runner.go           # MCP test runner implementation
├── scenarios/              # Test scenarios in YAML
│   ├── protocol.yaml      # Protocol compliance tests
│   ├── tools.yaml         # Tools functionality tests
│   ├── resources.yaml     # Resources functionality tests
│   └── prompts.yaml       # Prompts functionality tests
└── tests/                  # Test implementations
    └── mcp_integration_test.go  # Main test file
```

## Test Categories

### 1. Protocol Compliance Tests (`protocol.yaml`)

Tests core MCP protocol requirements:

- **Initialize**: Connection initialization and capability negotiation
- **Error Handling**: Proper error codes and messages
- **JSON-RPC Compliance**: Valid request/response format
- **State Management**: Initialization state tracking
- **Notifications**: Fire-and-forget messages

Example scenarios:
- `initialize_basic` - Standard initialization
- `initialize_unsupported_version` - Version negotiation
- `call_before_init` - State validation
- `invalid_jsonrpc` - Protocol format validation

### 2. Tools Tests (`tools.yaml`)

Tests MCP tools functionality:

- **Tool Discovery**: `tools/list` endpoint
- **Tool Invocation**: `tools/call` with various payloads
- **Input Validation**: Schema validation for tool arguments
- **Progress Tracking**: Progress notifications for long-running tools
- **Error Handling**: Invalid tool names and arguments

Example scenarios:
- `tools_list` - List all available tools
- `tool_analyze_text_sentiment` - Call sentiment analysis tool
- `tool_execute_code_python` - Execute Python code
- `tool_batch_with_progress` - Track batch processing progress

### 3. Resources Tests

Tests MCP resources functionality:

- **Resource Discovery**: `resources/list` endpoint
- **Resource Reading**: `resources/read` with URI templates
- **MIME Type Handling**: Proper content type support
- **Subscriptions**: Resource update subscriptions
- **URI Resolution**: Template and parameter handling

Example scenarios:
- `list_resources` - List all available resources
- `read_document_resource` - Read document content
- `read_system_info` - Get system information
- `subscribe_to_updates` - Subscribe to resource changes

### 4. Prompts Tests

Tests MCP prompts functionality:

- **Prompt Discovery**: `prompts/list` endpoint
- **Static Prompts**: Pre-defined prompt templates
- **Dynamic Prompts**: Context-aware prompt generation
- **Variable Substitution**: Template variable handling
- **Message Formatting**: Proper role and content structure

Example scenarios:
- `list_prompts` - List all available prompts
- `get_static_prompt` - Retrieve static prompt with variables
- `get_dynamic_prompt` - Generate context-aware prompt
- `get_invalid_prompt` - Error handling for missing prompts

## Adding New Test Scenarios

### 1. YAML Scenario Format

Create a new scenario in the appropriate YAML file:

```yaml
scenarios:
  - name: "my_test_scenario"
    method: "tools/call"
    request:
      name: "my_tool"
      arguments:
        param1: "value1"
        param2: 42
    validate:
      content:
        - type: "text"
          text: "Expected output"
    expectError:
      code: -32602
      message: "Error message"
```

### 2. Programmatic Scenarios

Add scenarios directly in Go code:

```go
scenarios := []engine.Scenario{
    {
        Name:   "custom_test",
        Method: "custom/method",
        Request: map[string]interface{}{
            "param": "value",
        },
        Validate: func(t *testing.T, response interface{}, err error) {
            require.NoError(t, err)
            
            result := response.(map[string]interface{})
            assert.Equal(t, "expected", result["field"])
        },
    },
}
```

### 3. Custom Validation Functions

Implement complex validation logic:

```go
func validateComplexResponse(t *testing.T, response interface{}, err error) {
    require.NoError(t, err)
    
    // Type assertions and validation
    result := response.(map[string]interface{})
    
    // Check structure
    assert.Contains(t, result, "requiredField")
    
    // Validate nested data
    nested := result["nested"].(map[string]interface{})
    assert.Equal(t, "value", nested["field"])
    
    // Check array elements
    items := result["items"].([]interface{})
    assert.Len(t, items, 3)
}
```

## Debugging Failed Tests

### 1. Enable Debug Output

```bash
TEST_DEBUG=true go test -v ./tests -run FailingTest
```

### 2. Keep Generated Code

```bash
TEST_KEEP_GENERATED=true go test ./tests
# Check the generated code in the temp directory printed
```

### 3. Check Server Logs

Server logs are written to `server-<port>.log` in the working directory:

```bash
cat /tmp/goa-test-*/server-*.log
```

### 4. Run Individual Scenarios

```bash
TEST_FILTER="specific_scenario_name" go test ./tests
```

### 5. Use External Server

Start your server manually and test against it:

```bash
# Start your MCP server
go run ./example/cmd/assistant --http-port 8080

# Run tests against it
TEST_SERVER_URL=http://localhost:8080 TEST_SKIP_GENERATION=true go test ./tests
```

## MCP Protocol Coverage

### Implemented Features

✅ **Core Protocol**
- Initialize handshake
- Capability negotiation
- JSON-RPC 2.0 compliance
- Error handling

✅ **Tools**
- Tool discovery (`tools/list`)
- Tool invocation (`tools/call`)
- Input schema validation
- Progress notifications

✅ **Resources**
- Resource discovery (`resources/list`)
- Resource reading (`resources/read`)
- URI template resolution
- MIME type handling

✅ **Prompts**
- Prompt discovery (`prompts/list`)
- Static prompts (`prompts/get`)
- Dynamic prompt generation
- Variable substitution

✅ **Notifications**
- Progress updates
- Status notifications
- Resource change events

### Pending Features

⏳ **Sampling** - Client LLM sampling requests
⏳ **Roots** - Filesystem/URI root discovery
⏳ **Logging** - Structured logging protocol
⏳ **Completion** - Autocomplete support

## Continuous Integration

### GitHub Actions

```yaml
name: MCP Integration Tests

on:
  push:
    branches: [main]
  pull_request:

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-go@v4
        with:
          go-version: '1.21'
      
      - name: Run Integration Tests
        run: |
          cd integration_tests
          go test -v ./tests
        env:
          TEST_PARALLEL: true
          TEST_TIMEOUT: 60s
```

### Local Pre-commit Hook

```bash
#!/bin/sh
# .git/hooks/pre-commit

echo "Running MCP integration tests..."
cd integration_tests && go test ./tests
if [ $? -ne 0 ]; then
    echo "Integration tests failed. Commit aborted."
    exit 1
fi
```

## Troubleshooting

### Common Issues

**Problem**: Tests fail with "server failed to become ready"
- **Solution**: Increase startup timeout or check server logs for errors

**Problem**: "method not found" errors
- **Solution**: Ensure DSL defines all required MCP methods

**Problem**: Type assertion panics in validation
- **Solution**: Add type checks before assertions or use type switches

**Problem**: Flaky tests with timing issues
- **Solution**: Add retries or increase timeouts for network operations

### Getting Help

1. Check server logs in the working directory
2. Enable debug output with `TEST_DEBUG=true`
3. Review the [MCP specification](https://modelcontextprotocol.io/specification)
4. Check the Goa test engine documentation
5. File an issue with reproduction steps

## Best Practices

1. **Group Related Tests**: Organize scenarios by feature area
2. **Use Descriptive Names**: Make test names self-documenting
3. **Validate Thoroughly**: Check both success and error paths
4. **Mock External Dependencies**: Use test doubles for external services
5. **Run Tests in CI**: Integrate with your CI/CD pipeline
6. **Document Custom Scenarios**: Add comments explaining complex tests
7. **Keep Scenarios Focused**: Test one thing per scenario
8. **Use Parallel Execution**: Speed up test runs where possible
9. **Clean Up Resources**: Ensure proper cleanup in test teardown
10. **Version Test Data**: Track test data changes with the code

## Contributing

When adding new MCP features:

1. Add test scenarios to the appropriate YAML file
2. Implement validation logic in the test file
3. Update this README with new test coverage
4. Ensure all tests pass before submitting PR
5. Add integration test results to PR description

## License

Same as the MCP plugin and Goa framework.