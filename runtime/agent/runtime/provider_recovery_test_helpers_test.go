package runtime

// Focused runtime tests use an explicit root port. It accepts local pause
// reporting and registration, but rejects attempts to contact a nonexistent
// parent. Actual parent binding and delivery are tested through engine adapters.

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/engine"
)

// recoveryContinuationWorkflow gives direct continuation tests the same
// restored allowance and callbacks that ExecuteWorkflow installs in production.
func recoveryContinuationWorkflow(t *testing.T, wf engine.WorkflowContext, checkpoint *workflowCheckpoint) *providerRecoveryWorkflowContext {
	t.Helper()
	wrapped, err := installProviderRecovery(wf, checkpoint.ProviderRecovery)
	require.NoError(t, err)
	return wrapped
}

type (
	testProviderRecoveryPort struct {
		accept engine.ProviderRecoveryAccept
		pause  engine.ProviderRecoveryPauseAccept
		parent bool
		report func(engine.ProviderRecoveryPaused) (engine.Future[struct{}], error)
	}
	testProviderRecoveryAcceptance struct{}
)

func (r *routeWorkflowContext) ProviderRecovery() engine.ProviderRecoveryPort {
	root := r.root()
	if root.recoveryPort == nil {
		root.recoveryPort = new(testProviderRecoveryPort)
	}
	return root.recoveryPort
}

func (t *testWorkflowContext) ProviderRecovery() engine.ProviderRecoveryPort {
	root := t.root()
	if root.recoveryPort == nil {
		root.recoveryPort = new(testProviderRecoveryPort)
	}
	return root.recoveryPort
}

func (p *testProviderRecoveryPort) HasParent() bool {
	return p.parent
}

func (p *testProviderRecoveryPort) Register(accept engine.ProviderRecoveryAccept) error {
	if p.accept != nil {
		return errors.New("recovery receiver already registered")
	}
	p.accept = accept
	return nil
}

func (p *testProviderRecoveryPort) RegisterPauseHandler(accept engine.ProviderRecoveryPauseAccept) error {
	if p.pause != nil {
		return errors.New("pause receiver already registered")
	}
	p.pause = accept
	return nil
}

func (p *testProviderRecoveryPort) Open(engine.WorkflowContext, engine.ProviderRecoveryFailure, engine.ProviderRecoveryReceive) (engine.ProviderRecovery, error) {
	return nil, errors.New("test root has no parent")
}

func (p *testProviderRecoveryPort) ReportPause(_ engine.WorkflowContext, pause engine.ProviderRecoveryPaused) (engine.Future[struct{}], error) {
	if p.report != nil {
		return p.report(pause)
	}
	return testProviderRecoveryAcceptance{}, nil
}

func (testProviderRecoveryAcceptance) IsReady() bool {
	return true
}

func (testProviderRecoveryAcceptance) Get(context.Context) (struct{}, error) {
	return struct{}{}, nil
}
