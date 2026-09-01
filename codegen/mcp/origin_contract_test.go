// This file verifies that generated MCP servers accept browser origins only
// when the application lists them while mounting the server.
package codegen

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/testutil"
	goacodegen "goa.design/goa/v3/codegen"
)

type (
	// originMountTemplateData contains the values used by the focused mount
	// template test.
	originMountTemplateData struct {
		MountServerDeclaration  *goacodegen.NameDeclaration
		ServerStructDeclaration *goacodegen.NameDeclaration
		Service                 originMountService
		Endpoints               []originMountEndpoint
		HasMixed                bool
		HasSSE                  bool
	}

	// originMountService names the generated MCP service and package.
	originMountService struct {
		Name    string
		PkgName string
	}

	// originMountEndpoint lists the HTTP routes mounted for one method.
	originMountEndpoint struct {
		Routes []originMountRoute
	}

	// originMountRoute is one generated MCP HTTP route.
	originMountRoute struct {
		Verb string
		Path string
	}
)

func TestMCPServerMountRendersExplicitOriginPolicy(t *testing.T) {
	generation, err := goacodegen.NewGeneration("example.com/calc/gen", nil)
	require.NoError(t, err)
	serverPackage, err := generation.ClaimPackage("example.com/calc/gen/jsonrpc/mcp_calc/server")
	require.NoError(t, err)
	mountDeclaration := goacodegen.NewExactName(goacodegen.NameFunction, "Mount")
	serverDeclaration := goacodegen.NewExactName(goacodegen.NameType, "Server")
	require.NoError(t, serverPackage.DeclareName(mountDeclaration))
	require.NoError(t, serverPackage.DeclareName(serverDeclaration))
	require.NoError(t, generation.Freeze())

	data := &originMountTemplateData{
		MountServerDeclaration:  mountDeclaration,
		ServerStructDeclaration: serverDeclaration,
		Service: originMountService{
			Name:    "mcp_calc",
			PkgName: "mcpcalc",
		},
		Endpoints: []originMountEndpoint{{
			Routes: []originMountRoute{{Verb: "POST", Path: "/mcp"}},
		}},
	}

	rendered := renderTemplateSection(t, "jsonrpc_server_mount", data)

	require.Contains(t, rendered, "func MountWithOrigins(")
	require.Contains(t, rendered, "origins []string")
	require.Contains(t, rendered, "_, ok := allowedOrigins[origin]")
	require.NotContains(t, rendered, "r.Host")
	testutil.AssertGo(t, "testdata/golden/origin_contract/server_mount.go.golden", rendered)
}
