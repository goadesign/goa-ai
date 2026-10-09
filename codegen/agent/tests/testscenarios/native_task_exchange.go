// Package testscenarios declares application-owned jobs consumed by generated
// native executors and registry providers. All values are synthetic contracts.
package testscenarios

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// NativeTaskExchange declares jobs with separate typed observation and answer
// methods. The polling variant exercises Goa's optional field default layout.
func NativeTaskExchange(defaultPolling bool) func() {
	return nativeTaskExchange(defaultPolling, false, false, false)
}

// NativeTaskExchangeWithInput asks the host before creating a durable job. Later
// reads, answers and cancellation retain the job instead of restarting creation.
func NativeTaskExchangeWithInput() func() {
	return nativeTaskExchange(false, true, false, false)
}

// NativeTaskExchangeSharedRoles lets two creators use the same observation and
// update methods while each tool retains its own original invocation.
func NativeTaskExchangeSharedRoles() func() {
	return nativeTaskExchange(false, false, true, false)
}

// NativeTaskExchangeViews exercises the normal service-selected view signature.
// Tool.Return still owns native tool output independently of its HTTP views.
func NativeTaskExchangeViews() func() {
	return nativeTaskExchange(false, false, false, true)
}

// nativeTaskExchange selects the creation result before evaluating the design.
func nativeTaskExchange(defaultPolling, beforeCreation, sharedCreators, viewed bool) func() {
	return func() {
		API("job_questions", func() {})
		empty := Type("Empty", func() {})
		key := Type("JobQuestionID", String)
		owner := Type("OwnerID", String, func() { MinLength(1) })
		form := Type("ProfileContent", func() {
			Field(1, "name", String, "Name supplied by the host.")
			Required("name")
		})
		accepted := Type("ProfileAccepted", func() {
			Field(1, "content", form, "Accepted profile values.")
			Required("content")
		})
		answer := Type("ProfileAnswer", func() {
			OneOf("answer", "Host response to the profile form.", func() {
				TypeName("ProfileDecision")
				Attribute("accept", accepted, "Accepted profile values.")
				Attribute("decline", empty, "Declined profile request.")
				Attribute("cancel", empty, "Cancelled profile request.")
			})
			Required("answer")
		})
		question := Type("ProfileQuestion", func() {
			Field(1, "message", String, "Question displayed to the host.", func() { Meta("struct:field:name", "PromptText") })
			Required("message")
		})
		approvalQuestion := Type("ApprovalQuestion", func() {
			Field(1, "message", String, "Consent requested from the host.")
			Field(2, "url", String, "External approval URL.", func() { Format(FormatURI) })
			Required("message", "url")
		})
		approvalAnswer := Type("ApprovalAnswer", func() {
			OneOf("answer", "Host decision for external approval.", func() {
				TypeName("ApprovalDecision")
				Attribute("accept", empty, "Approved interaction.")
				Attribute("decline", empty, "Declined interaction.")
				Attribute("cancel", empty, "Cancelled interaction.")
			})
			Required("answer")
		})
		request := Type("JobRequest", func() {
			OneOf("request", "Typed question issued by the job.", func() {
				TypeName("JobRequestKind")
				Attribute("profile", question, "Request a profile form.")
				Attribute("approval", approvalQuestion, "Request external approval.")
			})
			Required("request")
		})
		response := Type("JobResponse", func() {
			OneOf("response", "Typed answer submitted to the job.", func() {
				TypeName("JobResponseKind")
				Attribute("profile", answer, "Answer a profile form.")
				Attribute("approval", approvalAnswer, "Answer external approval.")
			})
			Required("response")
		})
		pending := Type("JobInput", func() {
			Field(1, "requests", MapOf(key, request), "Outstanding questions keyed by their lifetime identity.")
			Required("requests")
		})
		metadata := Type("JobMetadata", func() {
			Field(1, "taskId", String, "Job identifier.")
			Field(2, "createdAt", String, "Creation timestamp.")
			Field(3, "lastUpdatedAt", String, "Observation timestamp.")
			Field(4, "ttlMs", Int64, "Retention in milliseconds; absent means unlimited.")
			Field(5, "pollIntervalMs", Int64, "Guidance for the next observation.", func() {
				if defaultPolling {
					Default(3)
				}
			})
			Required("taskId", "createdAt", "lastUpdatedAt")
		})
		failure := Type("JobFailure", func() {
			Field(1, "code", Int, "JSON-RPC execution error code.")
			Field(2, "message", String, "Execution failure explanation.")
			Field(3, "data", Any, "Optional exact JSON error details.", func() { Meta("struct:field:type", "rawjson.Message", "goa.design/goa-ai/runtime/agent/rawjson") })
			Required("code", "message")
		})
		observationDSL := func() {
			Field(1, "task", metadata, "Current job identity and retention facts.")
			OneOf("outcome", "Current job state and its associated data.", func() {
				TypeName("JobState")
				Attribute("working", empty, "Work continues.")
				Attribute("input_required", pending, "Work waits for host answers.")
				Attribute("complete", String, "Finished report.")
				Attribute("failed", failure, "Execution ended with a protocol error.")
				Attribute("cancelled", empty, "Cancellation completed.")
			})
			Required("task", "outcome")
			if viewed {
				View("default", func() { Attribute("task"); Attribute("outcome") })
				View("alternate", func() { Attribute("task"); Attribute("outcome") })
			}
		}
		var observation any
		if viewed {
			observation = ResultType("application/vnd.job.observation", func() { TypeName("JobObservation"); observationDSL() })
		} else {
			observation = Type("JobObservation", observationDSL)
		}
		creationInput := Type("CreationInput", func() { Field(1, "state", String, "Exact host input state.") })
		creationPending := Type("CreationPending", func() { Field(1, "state", String, "State saved until the host resumes creation.") })
		creationResult := observation
		if beforeCreation {
			creationResult = Type("CreationOutcome", func() {
				OneOf("outcome", "Required input or the durably created job.", func() {
					TypeName("CreationState")
					Attribute("input_required", creationPending, "Host input precedes durable creation.")
					Attribute("complete", observation, "Durably created job.")
				})
				Required("outcome")
			})
		}
		Service("jobs", func() {
			Description("Owns durable report jobs, observes their state and accepts host decisions.")
			create := func() {
				Description("Starts a durable report job after any required host input.")
				Payload(func() {
					Field(1, "query", String, "Report to create.")
					Field(2, "owner", owner, "Owner whose job is created.", func() { Meta("struct:field:name", "OwnerContext") })
					if beforeCreation {
						Field(3, "continuation", creationInput, "Input supplied by the host before durable creation.", func() { Meta("struct:field:name", "HostInput") })
					}
					Required("query", "owner")
				})
				Result(creationResult)
				if beforeCreation {
					InputExchange("continuation", "outcome")
				}
				TaskExchange("read", "answer", "cancel")
			}
			Method("create", create)
			if sharedCreators {
				Method("create_shared", create)
			}
			Method("read", func() {
				Description("Observes an existing report job without repeating its creation.")
				Payload(func() {
					Field(1, "taskId", String, "Job to observe.")
					Field(2, "owner", owner, "Owner whose job is observed.", func() { Meta("struct:field:name", "OwnerIdentity") })
					Field(3, "selection", String, "Read profile used when creation has no selection.", func() { Default("standard"); Enum("standard") })
					Required("taskId", "owner")
				})
				Result(observation)
			})
			Method("answer", func() {
				Description("Submits typed host decisions for outstanding questions on an existing job.")
				Payload(func() {
					Field(1, "taskId", String, "Job receiving these answers.")
					Field(2, "responses", MapOf(key, response), "Answers to outstanding questions.", func() { Meta("struct:field:name", "HostAnswers") })
					Field(3, "owner", owner, "Owner whose job receives answers.", func() { Meta("struct:field:name", "OwnerIdentity") })
					Required("taskId", "responses", "owner")
				})
			})
			Method("cancel", func() {
				Description("Requests cancellation of an existing job; observation confirms its final state.")
				Payload(func() {
					Field(1, "taskId", String, "Job to cancel.")
					Field(2, "owner", owner, "Owner whose job is cancelled.", func() { Meta("struct:field:name", "OwnerIdentity") })
					Required("taskId", "owner")
				})
			})
			Agent("worker", "Creates reports with application-owned jobs.", func() {
				Use("reports", func() {
					Tool("create", "Create a report.", func() { BindTo("create") })
					if sharedCreators {
						Tool("create_shared", "Create another report using the same job lifecycle.", func() { BindTo("create_shared") })
					}
				})
			})
		})
	}
}
