package toolserverdata

import (
	"fmt"

	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

// Apply decodes a durable server-data envelope and delegates its closed
// tool-specific contract to the generated canonicalizer.
func Apply(canonicalize tools.ServerDataCanonicalizer, data rawjson.Message) (rawjson.Message, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if canonicalize == nil {
		return nil, fmt.Errorf("tool does not declare server data")
	}
	return canonicalize(data)
}
