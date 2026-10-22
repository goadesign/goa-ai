package temporal

// Native SDK handlers share the adapter's name reservations, queue workers,
// registration seal, and activation lifecycle with typed agent handlers. The
// accepted registration records also supply the exact queues a process serves.

import (
	"context"
	"errors"
	"reflect"
	"slices"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"

	"goa.design/goa-ai/runtime/agent/engine"
)

type (
	// handlerRegistration records successful or reserved queues for one name.
	// Native SDK handlers may repeat across queues; agent handlers may not.
	handlerRegistration struct {
		native bool
		queues map[string]bool
	}

	// activityRegistration keeps typed call defaults with the accepted queues.
	activityRegistration struct {
		handlerRegistration
		options engine.ActivityOptions
	}

	// RegisteredTaskQueue describes one queue with successfully registered handlers.
	// Kinds contains Workflow, Activity, or both using Temporal's task queue types.
	RegisteredTaskQueue struct {
		// Name is the effective queue after resolving the adapter's default.
		Name string
		// Kinds lists workflow before activity when both handler kinds are present.
		Kinds []enumspb.TaskQueueType
	}
)

// RegisterNativeWorkflow registers one named SDK workflow function without an agent
// RunInput or agent execution context. SDK options.Name is required. An empty
// queue uses the adapter default. Non-functions are rejected before registration;
// SDK function-signature validation retains SDK behavior.
func (e *Engine) RegisterNativeWorkflow(ctx context.Context, queue string, handler any, options workflow.RegisterOptions) error {
	if err := e.requireWorkerMode("register native workflows"); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if options.Name == "" {
		return errors.New("temporal engine: native workflow name cannot be empty")
	}
	if err := validateNativeFunction(handler); err != nil {
		return err
	}
	if queue == "" {
		queue = e.defaultQueue
	}
	if err := e.beginWorkflowRegistration(options.Name, queue, true); err != nil {
		return err
	}
	registered := false
	defer func() {
		if !registered {
			e.abortWorkflowRegistration(options.Name, queue)
		}
	}()
	bundle := e.workerForQueue(queue)
	bundle.worker.RegisterWorkflowWithOptions(handler, options)
	e.finishWorkflowRegistration(options.Name, queue, true)
	registered = true
	return nil
}

// RegisterNativeActivity registers one named SDK activity function without agent input
// or runtime-owned activity defaults. SDK options.Name is required. An empty
// queue uses the adapter default. Non-functions are rejected before registration;
// SDK function-signature validation retains SDK behavior.
func (e *Engine) RegisterNativeActivity(ctx context.Context, queue string, handler any, options activity.RegisterOptions) error {
	if err := e.requireWorkerMode("register native activities"); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if options.Name == "" {
		return errors.New("temporal engine: native activity name cannot be empty")
	}
	if err := validateNativeFunction(handler); err != nil {
		return err
	}
	if queue == "" {
		queue = e.defaultQueue
	}
	if err := e.beginActivityRegistration(options.Name, queue, true); err != nil {
		return err
	}
	registered := false
	defer func() {
		if !registered {
			e.abortActivityRegistration(options.Name, queue)
		}
	}()
	bundle := e.workerForQueue(queue)
	bundle.worker.RegisterActivityWithOptions(handler, options)
	e.finishActivityRegistration(options.Name, queue, engine.ActivityOptions{Queue: queue}, true)
	registered = true
	return nil
}

// RegisteredTaskQueues returns an independent, sorted inventory of successful
// workflow and activity registrations. It includes activity-only queues and
// excludes failed registrations, which own no handlers for a worker to serve.
func (e *Engine) RegisteredTaskQueues() []RegisteredTaskQueue {
	e.mu.Lock()
	defer e.mu.Unlock()

	kinds := make(map[string]map[enumspb.TaskQueueType]bool)
	for _, registered := range e.workflows {
		for queue := range registered.queues {
			if kinds[queue] == nil {
				kinds[queue] = make(map[enumspb.TaskQueueType]bool)
			}
			kinds[queue][enumspb.TASK_QUEUE_TYPE_WORKFLOW] = true
		}
	}
	for _, registered := range e.activityOptions {
		for queue := range registered.queues {
			if kinds[queue] == nil {
				kinds[queue] = make(map[enumspb.TaskQueueType]bool)
			}
			kinds[queue][enumspb.TASK_QUEUE_TYPE_ACTIVITY] = true
		}
	}
	names := make([]string, 0, len(kinds))
	for name := range kinds {
		names = append(names, name)
	}
	slices.Sort(names)
	queues := make([]RegisteredTaskQueue, 0, len(names))
	for _, name := range names {
		queue := RegisteredTaskQueue{Name: name}
		for _, kind := range []enumspb.TaskQueueType{enumspb.TASK_QUEUE_TYPE_WORKFLOW, enumspb.TASK_QUEUE_TYPE_ACTIVITY} {
			if kinds[name][kind] {
				queue.Kinds = append(queue.Kinds, kind)
			}
		}
		queues = append(queues, queue)
	}
	return queues
}

// registrationConflicts compares the accepted handler kind and queue with a
// requested registration, keeping typed names global and SDK names queue-local.
func registrationConflicts(registered handlerRegistration, queue string, native bool) bool {
	return len(registered.queues) > 0 && (!native || !registered.native || registered.queues[queue])
}

// removePendingRegistration releases one queue reservation and removes the
// name once no registration attempt still owns it. The caller holds e.mu.
func removePendingRegistration(pending map[string]handlerRegistration, name, queue string) {
	registered := pending[name]
	delete(registered.queues, queue)
	if len(registered.queues) == 0 {
		delete(pending, name)
	}
}

// validateNativeFunction keeps one supplied name equal to one SDK handler.
// Struct activity groups can install several method names before failing, so
// they cannot use this API's single-name reservation and queue inventory.
func validateNativeFunction(handler any) error {
	handlerType := reflect.TypeOf(handler)
	if handlerType == nil || handlerType.Kind() != reflect.Func {
		return errors.New("temporal engine: native registration requires one function handler")
	}
	return nil
}
