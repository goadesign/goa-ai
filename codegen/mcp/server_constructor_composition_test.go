// These tests compile MCP servers and native example startup from one required
// dependency plan. Generated clients then exercise the same guarded server
// through direct routing and mounting while supplied dependency values stay intact.
package codegen

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMCPServerConstructorUsesNativeDependencies(t *testing.T) {
	runMCPServerConstructorExample(t, constructorDependencyDesign, constructorDependencyRuntime)
}

func TestMCPResourceServerUsesNativeExampleDependencies(t *testing.T) {
	design, _ := jwtResourceFixture()
	runMCPServerConstructorExample(t, design, "")
}

// runMCPServerConstructorExample generates both the transport and application
// startup from the same dependency plan, then compiles all generated packages.
// An optional runtime test exercises the supplied dependencies through requests.
func runMCPServerConstructorExample(t *testing.T, design, runtime string) {
	t.Helper()
	directory := t.TempDir()
	module := fmt.Sprintf(`module constructor-probe.local

go 1.26.0

require (
 goa.design/goa-ai v0.0.0
 goa.design/goa/v3 v3.0.0
)

replace goa.design/goa-ai => %s
replace goa.design/goa/v3 => %s
`, filepath.ToSlash(testModuleDirectory(t, "goa.design/goa-ai")), filepath.ToSlash(testModuleDirectory(t, "goa.design/goa/v3")))
	require.NoError(t, os.Mkdir(filepath.Join(directory, "design"), 0o700))
	for name, source := range map[string]string{
		"go.mod":           module,
		"design/design.go": design,
	} {
		require.NoError(t, os.WriteFile(filepath.Join(directory, name), []byte(source), 0o600))
	}
	if runtime != "" {
		require.NoError(t, os.WriteFile(filepath.Join(directory, "dependency_test.go"), []byte(runtime), 0o600))
	}
	// The authored design declares dependencies before names freeze. Both commands
	// must use that plan so example startup calls the actual required constructor.
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	for _, arguments := range [][]string{
		{"run", "goa.design/goa/v3/cmd/goa", "gen", "constructor-probe.local/design"},
		{"run", "goa.design/goa/v3/cmd/goa", "example", "constructor-probe.local/design"},
		{"test", "-count=1", "-race", "-p=1", "./..."},
	} {
		// #nosec G204 -- arguments select fixed generator and test commands for this synthetic module.
		command := exec.CommandContext(ctx, "go", arguments...)
		command.Dir = directory
		command.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
		output, err := command.CombinedOutput()
		require.NoError(t, err, string(output))
	}
}

const constructorDependencyDesign = `package design

import (
	"cmp"
	"fmt"

	. "goa.design/goa-ai/dsl"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/generator"
	. "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/expr"
)

type dependencyOrder string

// ComparePackageName orders colliding factory names so both generation commands use the same names.
func (o dependencyOrder) ComparePackageName(other codegen.PackageNameOrder) int {
	return cmp.Compare(string(o), string(other.(dependencyOrder)))
}

func init() {
	for _, command := range []string{"gen", "example"} {
		generator.RegisterPluginLast("required-mcp-dependency", command, func() generator.Plugin {
			return generator.Plugin{Plan: declareDependencies}
		})
	}
}

// declareDependencies supplies the same imported dependency types to server and example generation.
func declareDependencies(plan *generator.Plan) error {
	transport, exists := plan.JSONRPC(expr.Root)
	if !exists {
		return fmt.Errorf("JSON-RPC plan missing")
	}
	for _, service := range expr.Root.API.JSONRPC.Services {
		if service.Name() != "mcp_records" {
			continue
		}
		for _, dependency := range []struct{ name, reference, importPath, alias string }{
			{"location", "*url.URL", "net/url", "url"},
			{"client", "*http.Client", "net/http", "http"},
		} {
			layout, err := codegen.PlanGoType(&expr.AttributeExpr{
				Type: expr.String,
				Meta: expr.MetaExpr{"struct:field:type": {dependency.reference, dependency.importPath, dependency.alias}},
			}, codegen.GoTypePlanOptions{Owner: "constructor-probe.local"})
			if err != nil {
				return err
			}
			if _, err := transport.DeclareServerConstructorDependency(service, dependency.name, layout, "NewRecords", dependencyOrder(dependency.name)); err != nil {
				return err
			}
		}
	}
	return nil
}

var _ = API("records", func() { Description("Exercise shared server construction") })
var _ = Service("records", func() {
	Description("Read a synthetic value through MCP")
	MCP("records", "1")
	JSONRPC(func() { POST("/mcp") })
	Method("read", func() {
		Description("Return one synthetic value")
		Result(String)
		Tool("read", "Read one value")
		JSONRPC(func() {})
	})
})
`

const constructorDependencyRuntime = `// These tests use generated MCP clients to verify the dependency values and
// browser policy retained by the shared native server constructor.
package recordsapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	genmcpclient "constructor-probe.local/gen/jsonrpc/mcp_records/client"
	genmcpserver "constructor-probe.local/gen/jsonrpc/mcp_records/server"
	genmcp "constructor-probe.local/gen/mcp_records"
	genrecords "constructor-probe.local/gen/records"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	goahttp "goa.design/goa/v3/http"
)

type (
	fixtureService struct{ calls atomic.Int32 }
	originDoer     struct {
		client *http.Client
		origin string
	}
)

// Read counts an accepted call and returns the synthetic service result.
func (s *fixtureService) Read(context.Context) (string, error) {
	s.calls.Add(1)
	return "accepted", nil
}

// Do adds the browser origin to a generated request and returns the HTTP response.
func (d originDoer) Do(request *http.Request) (*http.Response, error) {
	request.Header.Set("Origin", d.origin)
	return d.client.Do(request)
}

func TestRequiredDependenciesAndOrigins(t *testing.T) {
	for _, mounted := range []bool{false, true} {
		name := "direct"
		if mounted {
			name = "mounted"
		}
		t.Run(name, func(t *testing.T) {
			service := &fixtureService{}
			mux := goahttp.NewMuxer()
			client := &http.Client{}
			location := &url.URL{Scheme: "https", Host: "resource.example"}
			adapter := genmcp.NewMCPAdapter(genrecords.NewEndpoints(service), nil)
			server := genmcpserver.New(genmcp.NewEndpoints(adapter), mux, goahttp.RequestDecoder, goahttp.ResponseEncoder, nil, client, location, "https://first.example", "https://client.example")
			signature := reflect.TypeOf(genmcpserver.New)
			assert.Equal(t, 8, signature.NumIn())
			assert.True(t, signature.IsVariadic())
			assert.Equal(t, reflect.TypeOf(client), signature.In(5))
			assert.Equal(t, reflect.TypeOf(location), signature.In(6))
			fields := reflect.ValueOf(server).Elem()
			assert.Equal(t, reflect.ValueOf(client).Pointer(), fields.FieldByName("client").Pointer())
			assert.Equal(t, reflect.ValueOf(location).Pointer(), fields.FieldByName("location").Pointer())
			if mounted {
				server.Mount(mux)
			} else {
				mux.Handle("POST", "/mcp", server.ServeHTTP)
			}
			peer := httptest.NewServer(mux)
			t.Cleanup(peer.Close)
			for _, origin := range []string{"https://first.example", "https://client.example"} {
				caller := genmcpclient.NewClient("http", strings.TrimPrefix(peer.URL, "http://"), originDoer{client: peer.Client(), origin: origin}, goahttp.RequestEncoder, goahttp.ResponseDecoder, false)
				value, err := caller.ToolsCall()(t.Context(), &genmcp.ToolsCallPayload{Name: "read"})
				require.NoError(t, err)
				result, ok := value.(*genmcp.ToolsCallResult).Outcome.AsComplete()
				require.True(t, ok)
				assert.JSONEq(t, "\"accepted\"", string(result.StructuredContent))
			}
			rejected := genmcpclient.NewClient("http", strings.TrimPrefix(peer.URL, "http://"), originDoer{client: peer.Client(), origin: "https://rejected.example"}, goahttp.RequestEncoder, goahttp.ResponseDecoder, false)
			_, err := rejected.ToolsCall()(t.Context(), &genmcp.ToolsCallPayload{Name: "read"})
			assert.Error(t, err)
			assert.Equal(t, int32(2), service.calls.Load())
		})
	}
}
`
