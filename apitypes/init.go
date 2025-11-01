package apitypes

import (
	goacodegen "goa.design/goa/v3/codegen"

	"goa.design/goa-ai/codegen/agents"
)

// init registers the agents plugin with Goa.
func init() {
	// Register Prepare for gen so we can force-generate required user types
	// before core codegen runs.
	goacodegen.RegisterPlugin("agents", "gen", codegen.Prepare, codegen.Generate)
	goacodegen.RegisterPlugin("agents", "example", nil, codegen.GenerateExample)
}
