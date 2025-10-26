package codegen

import (
	"goa.design/goa/v3/codegen"
)

// init registers the agents plugin with Goa.
func init() {
	codegen.RegisterPlugin("agents", "gen", nil, Generate)
	// Register example-phase hooks to emit bootstrap helper and planner stubs.
	codegen.RegisterPlugin("agents", "example", nil, ModifyExampleFiles)
}
