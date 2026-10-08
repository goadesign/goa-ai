// Package dsl declares application-owned jobs consumed by generated tool
// executors and MCP adapters. The application keeps job state and effects;
// generated callers use the existing read, answer and cancel methods.
package dsl

import (
	"goa.design/goa-ai/internal/mcpinput"
	"goa.design/goa/v3/eval"
	"goa.design/goa/v3/expr"
)

// TaskExchange binds an asynchronous creator to its read, answer and cancel
// methods in the same Goa service. Creation and reading return the same named
// observation type: required task metadata and an outcome OneOf. The outcome
// selects working, input_required, complete, failed or cancelled. Only complete
// enters the model's result contract. The service durably creates the job before
// returning its first observation and authorizes every later operation.
//
// Metadata declares required taskId, createdAt and lastUpdatedAt strings and
// optional statusMessage, ttlMs and pollIntervalMs. A nil ttlMs means unlimited
// retention. Read and cancel take taskId; answer also takes a typed responses
// map matching input_required requests. Map values select matching typed
// request and response unions, with one branch per question kind. Answers use
// accept, decline and cancel, as in InputExchange. The service issues map keys
// that cannot be reused after an answer within one job. Answer and cancel return
// no domain result.
//
// The failed branch declares required Int code and String message fields. Its
// optional data field declares Any with Meta("struct:field:type", "rawjson.Message",
// "goa.design/goa-ai/runtime/agent/rawjson") so error details preserve exact JSON.
//
// An InputExchange may precede creation; its complete branch then contains the
// job observation. MCP clients must declare Tasks support before the creator
// runs. Local BindTo executors and registry providers retain the same job and
// use workflow-owned observation and host-answer operations.
func TaskExchange(read, answer, cancel string) {
	method, ok := eval.Current().(*expr.MethodExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	if read == "" || answer == "" || cancel == "" {
		eval.ReportError("TaskExchange requires read, answer and cancel method names")
		return
	}
	if _, exists := method.Meta[mcpinput.TaskExchangeMetaKey]; exists {
		eval.ReportError("TaskExchange may appear only once per method")
		return
	}
	if method.Meta == nil {
		method.Meta = make(expr.MetaExpr)
	}
	method.Meta[mcpinput.TaskExchangeMetaKey] = []string{read, answer, cancel}
}
