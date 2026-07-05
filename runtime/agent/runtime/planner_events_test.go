package runtime

// planner_events_test.go tests runtimePlannerEvents' tool-call signature side
// carry in isolation from the model-client capture wrapper (see
// model_wrapper_test.go for capture-side coverage) and from the transcript
// rebuild lookup (see workflow_helpers_test.go for lookup-side coverage).

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRuntimePlannerEventsExportToolCallSignaturesReturnsNilWhenEmpty(t *testing.T) {
	e := &runtimePlannerEvents{}
	require.Nil(t, e.exportToolCallSignatures())
}

func TestRuntimePlannerEventsRecordToolCallSignatureExportsSnapshot(t *testing.T) {
	e := &runtimePlannerEvents{}
	e.recordToolCallSignature("call-1", "sig-1")
	e.recordToolCallSignature("call-2", "sig-2")

	got := e.exportToolCallSignatures()
	require.Equal(t, map[string]string{"call-1": "sig-1", "call-2": "sig-2"}, got)

	// The exported map is a snapshot copy: mutating it must not corrupt the
	// sink's internal state for subsequent exports.
	got["call-1"] = "mutated"
	require.Equal(t, "sig-1", e.exportToolCallSignatures()["call-1"])
}

func TestRuntimePlannerEventsRecordToolCallSignatureOverwritesByID(t *testing.T) {
	e := &runtimePlannerEvents{}
	e.recordToolCallSignature("call-1", "sig-1")
	e.recordToolCallSignature("call-1", "sig-2")

	require.Equal(t, map[string]string{"call-1": "sig-2"}, e.exportToolCallSignatures())
}
