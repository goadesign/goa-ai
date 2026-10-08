// These tests evaluate authored task subscription designs. Invalid creator
// selections and event shapes must fail before any server code is generated.
package dsl_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

func TestMCPTaskSubscription(t *testing.T) {
	runMCPDSL(t, func() { taskSubscriptionDesign("") })
}

func TestMCPTaskSubscriptionRejectsInvalidDeclarations(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"unknown creator", "unknown creator method"},
		{"ordinary method", "TaskExchange creator exposed as an MCP tool"},
		{"unexposed creator", "TaskExchange creator exposed as an MCP tool"},
		{"required tasks", "optional object"},
		{"required job array", "optional array"},
		{"scalar job selection", "optional array"},
		{"mismatched acknowledgment", "omit creator"},
		{"missing task updates", `must declare "tasks_updated"`},
		{"unselected resource updates", "unsupported branch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := runMCPDSLWithError(t, func() { taskSubscriptionDesign(tc.name) })
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

// taskSubscriptionDesign declares the same job and typed question contracts
// for every case, changing only the source contract named by the test.
func taskSubscriptionDesign(mode string) {
	API("test", func() {})
	empty := Type("Empty", func() {})
	content := Type("AnswerContent", func() { Attribute("label", String, "Requested label"); Required("label") })
	request := Type("Question", func() { Attribute("message", String, "Question for the host"); Required("message") })
	response := Type("Answer", func() {
		OneOf("answer", "Host response", func() {
			Attribute("accept", func() { Attribute("content", content, "Accepted label"); Required("content") })
			Attribute("decline", empty, "Host declined")
			Attribute("cancel", empty, "Host cancelled")
		})
		Required("answer")
	})
	requestRecord := Type("RequestRecord", func() {
		OneOf("request", "Question kind", func() { Attribute("details", request, "Label question") })
		Required("request")
	})
	responseRecord := Type("ResponseRecord", func() {
		OneOf("response", "Response kind", func() { Attribute("details", response, "Label response") })
		Required("response")
	})
	pending := Type("Pending", func() {
		Attribute("requests", MapOf(String, requestRecord), "Outstanding questions")
		Required("requests")
	})
	metadata := Type("Metadata", func() {
		Attribute("taskId", String, "Native job identity")
		Attribute("createdAt", String, "Creation time")
		Attribute("lastUpdatedAt", String, "Observation time")
		Required("taskId", "createdAt", "lastUpdatedAt")
	})
	failure := Type("Failure", func() {
		Attribute("code", Int, "Execution error code")
		Attribute("message", String, "Execution error message")
		Required("code", "message")
	})
	observation := Type("Observation", func() {
		Attribute("task", metadata, "Job metadata")
		OneOf("outcome", "Current job state", func() {
			Attribute("working", empty, "Job continues")
			Attribute("input_required", pending, "Host input needed")
			Attribute("complete", String, "Finished label")
			Attribute("failed", failure, "Execution failed")
			Attribute("cancelled", empty, "Cancellation finished")
		})
		Required("task", "outcome")
	})
	creator := "create"
	if mode == "unknown creator" {
		creator = "missing"
	}
	if mode == "ordinary method" {
		creator = "read"
	}
	ids := any(ArrayOf(String))
	if mode == "scalar job selection" {
		ids = String
	}
	selections := Type("Selections", func() {
		Attribute(creator, ids, "Native job selections")
		if mode == "required job array" {
			Required(creator)
		}
	})
	accepted := selections
	if mode == "mismatched acknowledgment" {
		accepted = Type("OtherSelections", func() { Attribute("read", ArrayOf(String), "Other native job selections") })
	}
	acknowledged := Type("Acknowledged", func() { Attribute("tasks", accepted, "Authorized native jobs") })
	updates := Type("Updates", func() { Attribute("tasks", selections, "Changed native jobs"); Required("tasks") })
	Service("jobs", func() {
		MCP("jobs", "1")
		JSONRPC(func() { POST("/mcp") })
		Method("create", func() {
			Result(observation)
			TaskExchange("read", "answer", "cancel")
			if mode != "unexposed creator" {
				Tool("create", "Create a durable job")
			}
		})
		Method("read", func() {
			Payload(func() { Attribute("taskId", String, "Native job identity"); Required("taskId") })
			Result(observation)
		})
		Method("answer", func() {
			Payload(func() {
				Attribute("taskId", String, "Native job identity")
				Attribute("responses", MapOf(String, responseRecord), "Host responses")
				Required("taskId", "responses")
			})
		})
		Method("cancel", func() { Payload(func() { Attribute("taskId", String, "Native job identity"); Required("taskId") }) })
		Method("watch", func() {
			Payload(func() {
				Attribute("tasks", selections, "Requested native jobs")
				if mode == "required tasks" {
					Required("tasks")
				}
			})
			StreamingResult(func() {
				OneOf("change", "Accepted selection or changed jobs", func() {
					Attribute("acknowledged", acknowledged, "Authorized native jobs")
					if mode != "missing task updates" {
						Attribute("tasks_updated", updates, "Changed native jobs")
					}
					if mode == "unselected resource updates" {
						Attribute("updated", empty, "Unselected resource change")
					}
				})
				Required("change")
			})
			SubscriptionSource()
		})
	})
}
