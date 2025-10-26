package assistantapi

import (
	orchestrator "example.com/assistant/gen/orchestrator"
)

// orchestrator service example implementation.
// The example methods log the requests and return zero values.
type orchestratorsrvc struct{}

// NewOrchestrator returns the orchestrator service implementation.
func NewOrchestrator() orchestrator.Service {
	return &orchestratorsrvc{}
}
