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
	"goa.design/goa-ai/runtime/agent/policy"
)

// newRunPolicyData copies the evaluated DSL run policy into immutable template
// data. It also resolves the recovery limit shown in generated documentation
// without changing the authored value emitted into agent registration code.
func newRunPolicyData(expr *agentsExpr.RunPolicyExpr) RunPolicyData {
	rp := RunPolicyData{
		Caps: CapsData{
			EffectiveMaxRecoveryTurns: policy.DefaultMaxRecoveryTurns,
		},
	}
	if expr == nil {
		return rp
	}
	rp.TimeBudget = expr.TimeBudget
	rp.PlanTimeout = expr.PlanTimeout
	rp.ToolTimeout = expr.ToolTimeout
	rp.OnMissingFields = expr.OnMissingFields
	if expr.History != nil {
		h := &HistoryData{
			Mode:                     string(expr.History.Mode),
			KeepRecent:               expr.History.KeepRecent,
			CompressAtTurns:          expr.History.CompressAtTurns,
			CompressAtMaxInputTokens: expr.History.CompressAtMaxInputTokens,
			KeepMaxTurns:             expr.History.KeepMaxTurns,
			KeepMaxInputTokens:       expr.History.KeepMaxInputTokens,
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
		rp.Caps.MaxToolCalls = expr.DefaultCaps.MaxToolCalls
		rp.Caps.MaxRecoveryTurns = expr.DefaultCaps.MaxRecoveryTurns
		if expr.DefaultCaps.MaxRecoveryTurns > 0 {
			rp.Caps.EffectiveMaxRecoveryTurns = expr.DefaultCaps.MaxRecoveryTurns
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
		artifact.RetryPolicy = plannerActivityRetryPolicy()
		artifact.StartToCloseTimeout = defaultPlannerActivityTimeout
	case ActivityKindExecuteTool:
		// ExecuteTool retries are safe because logical tool calls now carry stable
		// identities and runtimes/providers are responsible for replaying durable
		// results instead of re-running side effects on retried attempts.
		artifact.RetryPolicy = retriedActivityPolicy()
	}
	return artifact
}

// plannerActivityRetryPolicy prevents infrastructure retries from repeating a
// model call after that attempt has streamed browser-visible text.
func plannerActivityRetryPolicy() engine.RetryPolicy {
	return engine.RetryPolicy{MaxAttempts: 1}
}

// retriedActivityPolicy returns the shared retry profile for generated
// activities whose effects have stable replay identities.
func retriedActivityPolicy() engine.RetryPolicy {
	return engine.RetryPolicy{
		MaxAttempts:        3,
		InitialInterval:    time.Second,
		BackoffCoefficient: 2,
	}
}
