package runtime

import "context"

// CallerFunc adapts a function to implement Caller.
type CallerFunc func(ctx context.Context, req CallRequest) (CallResponse, error)

// CallTool implements Caller.
func (f CallerFunc) CallTool(ctx context.Context, req CallRequest) (CallResponse, error) {
	return f(ctx, req)
}
