// Package codegen isolates runtime policy artifacts from structural generator
// data assembly.
//
// This file owns the small cluster of helpers that translate DSL run-policy
// settings into activity/runtime metadata. Keeping them separate lets `data.go`
// stay focused on shape assembly while preserving the same package-local
// contracts and defaults for activity generation.
package codegen

import (
	"fmt"
	"strings"
	"time"

	"goa.design/goa-ai/codegen/naming"
	agentsExpr "goa.design/goa-ai/expr/agent"
	"goa.design/goa-ai/runtime/agent/engine"
)

// newRunPolicyData copies the evaluated DSL run policy into immutable template
// data, preserving only the fields that affect generated runtime wiring.
func newRunPolicyData(expr *agentsExpr.RunPolicyExpr) RunPolicyData {
	if expr == nil {
		return RunPolicyData{}
	}
	rp := RunPolicyData{
		TimeBudget:        expr.TimeBudget,
		PlanTimeout:       expr.PlanTimeout,
		ToolTimeout:       expr.ToolTimeout,
		InterruptsAllowed: expr.InterruptsAllowed,
		OnMissingFields:   expr.OnMissingFields,
	}
	if expr.History != nil {
		h := &HistoryData{
			Mode:               string(expr.History.Mode),
			KeepRecent:         expr.History.KeepRecent,
			TriggerAt:          expr.History.TriggerAt,
			CompressKeepRecent: expr.History.CompressKeepRecent,
		}
		rp.History = h
	}
	if expr.Cache != nil {
		rp.Cache = CacheData{
			AfterSystem: expr.Cache.AfterSystem,
			AfterTools:  expr.Cache.AfterTools,
		}
	}
	if expr.DefaultCaps != nil {
		rp.Caps = CapsData{
			MaxToolCalls:                  expr.DefaultCaps.MaxToolCalls,
			MaxConsecutiveFailedToolCalls: expr.DefaultCaps.MaxConsecutiveFailedToolCall,
		}
	}
	return rp
}

// newActivity derives the generated activity names, function identifiers, and
// retry policy from one logical agent runtime activity.
func newActivity(agent *AgentData, kind ActivityKind, logicalSuffix string, queue string) ActivityArtifact {
	funcName := fmt.Sprintf("%s%sActivity", agent.GoName, logicalSuffix)
	definitionVar := fmt.Sprintf("%s%sActivityDefinition", agent.GoName, logicalSuffix)
	name := naming.Identifier(agent.Service.Name, agent.Name, strings.ToLower(logicalSuffix))
	artifact := ActivityArtifact{
		Name:          name,
		FuncName:      funcName,
		DefinitionVar: definitionVar,
		Queue:         queue,
		Kind:          kind,
	}
	switch kind {
	case ActivityKindPlan, ActivityKindResume:
		artifact.RetryPolicy = defaultActivityRetryPolicy()
		artifact.StartToCloseTimeout = defaultPlannerActivityTimeout
	case ActivityKindExecuteTool:
		// ExecuteTool retries are safe because logical tool calls now carry stable
		// identities and runtimes/providers are responsible for replaying durable
		// results instead of re-running side effects on retried attempts.
		artifact.RetryPolicy = defaultActivityRetryPolicy()
	}
	return artifact
}

// defaultActivityRetryPolicy returns the shared retry profile for generated
// planner/runtime activities.
func defaultActivityRetryPolicy() engine.RetryPolicy {
	return engine.RetryPolicy{
		MaxAttempts:        3,
		InitialInterval:    time.Second,
		BackoffCoefficient: 2,
	}
}
