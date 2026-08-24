// This file registers new agent plugins for each Goa generation command.
package codegen

import (
	goagenerator "goa.design/goa/v3/codegen/generator"
)

func init() {
	goagenerator.RegisterPluginFirst("agent", "gen", newAgentPluginFactory(false))
	goagenerator.RegisterPlugin("agent", "example", newAgentPluginFactory(true))
}
