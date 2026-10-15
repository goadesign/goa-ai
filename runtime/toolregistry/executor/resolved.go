// Package executor runs generated service tools against the exact registration
// retained with their call, preserving generated result codecs and metadata.
package executor

import (
	"context"
	"fmt"
	"slices"

	pulsec "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	"goa.design/goa-ai/internal/registrycall"
	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/runtime"
)

type resolvedExecutor struct {
	registry string
	client   *genregistry.Client
	pulse    pulsec.Client
	specs    SpecLookup
	options  []Option
}

// NewResolved constructs an executor for generated service tools whose calls
// retain an owner-selected registration. Each call uses its original route,
// operation and token for admission and retries, and the supplied generated codecs.
func NewResolved(registry string, client *genregistry.Client, pulse pulsec.Client, specs SpecLookup, opts ...Option) (runtime.ToolCallExecutor, error) {
	if registry == "" || client == nil || pulse == nil || specs == nil {
		return nil, fmt.Errorf("resolved executor requires registry, client, stream client and generated specifications")
	}
	return &resolvedExecutor{registry: registry, client: client, pulse: pulse, specs: specs, options: slices.Clone(opts)}, nil
}

// Execute reads only the saved selection and sends the unchanged invocation
// through the shared admission and result-stream implementation.
func (e *resolvedExecutor) Execute(ctx context.Context, meta *runtime.ToolCallMeta, call *runtime.ToolCall) (*runtime.ToolExecutionResult, error) {
	if call == nil || call.Registry == nil || call.Registry.Registry != e.registry {
		return nil, fmt.Errorf("resolved executor requires a saved binding for registry %q", e.registry)
	}
	resolved, err := registrycontract.Read(call.Registry)
	if err != nil {
		return nil, err
	}
	declaration, err := resolved.Declaration(call.Name)
	if err != nil {
		return nil, err
	}
	if resolved.Source != nil || declaration.ConsumerContract.Kind != "service" {
		return nil, fmt.Errorf("resolved executor requires a generated service binding")
	}
	if _, exists := e.specs.Spec(call.Name); !exists {
		return nil, fmt.Errorf("resolved executor has no generated contract for %q", call.Name)
	}
	client := registrycall.NewResolvedClient(e.client, resolved.Registered.RegistrationToken, declaration.Name)
	executor, err := New(client, e.pulse, resolved.Registered.Toolset.Name, e.specs, e.options...)
	if err != nil {
		return nil, err
	}
	return executor.Execute(ctx, meta, call)
}
