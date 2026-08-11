// Package design defines a small evaluation suite used to prove that an
// external Goa application can generate and consume Goa-AI eval code.
package design

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa-ai/eval/dsl"
	. "goa.design/goa/v3/dsl"
)

var ChatEvalTarget = Type("ChatEvalTarget", func() {
	Attribute("org_id", String, "Organization identifier.", func() {
		Format(FormatUUID)
	})
	Attribute("agent_id", String, "Facility agent identifier.", func() {
		Format(FormatUUID)
	})
	Attribute("user_id", String, "User identifier.", func() {
		Format(FormatUUID)
	})
	Required("org_id", "agent_id", "user_id")
})

var ChatEvalInput = Type("ChatEvalInput", func() {
	Attribute("target", ChatEvalTarget, "Principal and facility under evaluation.")
	Attribute("prompt", String, "User message.", func() {
		MinLength(1)
	})
	Required("target", "prompt")
})

var PublishedTaskEvalInput = Type("PublishedTaskEvalInput", func() {
	Attribute("target", ChatEvalTarget, "Principal and facility under evaluation.")
	Attribute("task_id", String, "Published Task identifier.", func() {
		Format(FormatUUID)
	})
	Required("target", "task_id")
})

// ChatEvalSuite declares the downstream twenty-one-scenario acceptance suite.
func ChatEvalSuite() {
	Suite("chat_quality", func() {
		Description("Checks the complete twenty-one-case Aura Chat evaluation shape.")
		Timeout("10m")

		chatScenario("alarm_activation_snapshot_summary", "Summarizes one alarm activation snapshot.", "2m")
		chatScenario("all_compressors_source_resolution", "Resolves sources for every compressor.", "3m")
		chatScenario("app_runtime_capability_agent_attribution", "Preserves edge-agent attribution.", "90s")
		Scenario("broad_refrigeration_task_preview", func() {
			Description("Runs the published refrigeration Task.")
			Input(PublishedTaskEvalInput)
			Tags("chat", "task")
			Timeout("10m")
		})
		chatScenario("compressor_measured_status", "Reports measured compressor status.", "90s")
		chatScenario("compressor_mode_change_confirmation", "Stops at compressor command confirmation.", "90s")
		chatScenario("compressor_running_activity", "Evaluates compressor running activity.", "3m")
		chatScenario("conveyor_blackbelt_root_cause", "Finds supported conveyor condition sources.", "3m")
		chatScenario("defrost_activity_during_peak_hours", "Evaluates bounded defrost activity.", "3m")
		chatScenario("defrost_mode_documentation_settings", "Continues from documentation to settings.", "150s")
		chatScenario("dock_door_semantic_absence", "Reports absent dock-door instrumentation.", "6m")
		chatScenario("equipment_status_synthesized_result", "Synthesizes nested equipment status.", "10m")
		chatScenario("evaporator_14c_pid_tuning", "Assesses evaporator PID tuning.", "90s")
		chatScenario("evaporator_alarm_pagination", "Follows every evaporator alarm page.", "10m")
		chatScenario("facility_wide_defrost_cycle_review", "Reviews facility-wide defrost cycles.", "10m")
		chatScenario("jim_frye_manual_setting_changes", "Audits one operator's setting changes.", "3m")
		chatScenario("large_control_context", "Uses an untruncated control narrative.", "90s")
		chatScenario("multi_device_key_events", "Retrieves consolidated key events.", "2m")
		chatScenario("room_condition_patterns", "Analyzes all requested room conditions.", "3m")
		chatScenario("single_equipment_defrost_cycle_review", "Resolves an exact equipment display name.", "90s")
		chatScenario("solar_load_shifting_savings_review", "Completes a solar savings assessment.", "10m")
	})
}

func chatScenario(name, description, timeout string) {
	Scenario(name, func() {
		Description(description)
		Input(ChatEvalInput)
		Tags("chat")
		Timeout(timeout)
	})
}
