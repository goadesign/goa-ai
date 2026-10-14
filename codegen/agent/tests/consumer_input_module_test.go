// Published contract tests load a provider's DSL from a versioned module archive
// and generate a separate consumer without access to a provider implementation.
package tests

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConsumerInputFromPublishedContractModule(t *testing.T) {
	const module = "contracts.example/records"
	const version = "v1.0.1"
	root := t.TempDir()
	proxy := filepath.Join(t.TempDir(), "proxy")
	versions := filepath.Join(proxy, module, "@v")
	require.NoError(t, os.MkdirAll(versions, 0700))
	mod := "module " + module + "\n\ngo 1.27.0\n\nrequire goa.design/goa-ai v0.0.0\n"
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for _, file := range []struct{ name, contents string }{{"go.mod", mod}, {"contract.go", publishedInputContract}} {
		entry, err := writer.Create(module + "@" + version + "/" + file.name)
		require.NoError(t, err)
		_, err = entry.Write([]byte(file.contents))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	zipBytes := archive.Bytes()
	archiveHash := sha256.Sum256(zipBytes)
	for _, file := range []struct {
		suffix string
		data   []byte
	}{
		{".zip", zipBytes}, {".mod", []byte(mod)},
		{".info", []byte(`{"Version":"v1.0.1","Time":"2026-01-01T00:00:00Z"}`)},
	} {
		require.NoError(t, os.WriteFile(filepath.Join(versions, version+file.suffix), file.data, 0600))
	}
	require.NoError(t, os.WriteFile(filepath.Join(versions, "list"), []byte(version+"\n"), 0600))
	framework, err := filepath.Abs("../../..")
	require.NoError(t, err)
	// The framework change is not released yet. Only the framework uses the
	// checkout; the provider contract must be downloaded from its archive.
	consumerMod := "module consumer.example/app\n\ngo 1.27.0\n\nrequire (\n" + module + " " + version + "\ngoa.design/goa-ai v0.0.0\n)\n\nreplace goa.design/goa-ai => " + filepath.ToSlash(framework) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte(consumerMod), 0600))
	require.NoError(t, os.Mkdir(filepath.Join(root, "design"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "design", "design.go"), []byte(publishedInputConsumer), 0600))
	proxyURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(proxy)}).String()
	env := append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod", "GOPROXY="+proxyURL+",https://proxy.golang.org", "GONOSUMDB="+module, "GONOPROXY=none")
	run := func(args ...string) []byte {
		t.Helper()
		// #nosec G204 -- arguments come from the fixed run calls below and select generator, module listing, and test commands.
		cmd := exec.CommandContext(t.Context(), "go", args...)
		cmd.Dir, cmd.Env = root, env
		output, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "go %s failed:\n%s", strings.Join(args, " "), output)
		return output
	}
	run("run", "goa.design/goa/v3/cmd/goa", "gen", "consumer.example/app/design")
	var downloaded struct {
		Path, Version, Dir string
		Replace            json.RawMessage
	}
	require.NoError(t, json.Unmarshal(run("list", "-m", "-json", module), &downloaded))
	assert.Equal(t, module, downloaded.Path)
	assert.Equal(t, version, downloaded.Version)
	assert.Empty(t, downloaded.Replace)
	contract, err := os.ReadFile(filepath.Join(downloaded.Dir, "contract.go"))
	require.NoError(t, err)
	assert.Equal(t, sha256.Sum256([]byte(publishedInputContract)), sha256.Sum256(contract))
	bindingPath := filepath.Join(root, "gen", "reader", "agents", "bound", "records", "inputbinding")
	require.NoError(t, os.WriteFile(filepath.Join(bindingPath, "published_test.go"), []byte(publishedInputBehavior), 0600))
	run("test", "-count=1", "./gen/reader/agents/bound/records/inputbinding")
	// #nosec G304 -- versions is built under this test's temporary directory; the module and archive version are constants above.
	storedArchive, err := os.ReadFile(filepath.Join(versions, version+".zip"))
	require.NoError(t, err)
	assert.Equal(t, archiveHash, sha256.Sum256(storedArchive))
}

const publishedInputContract = `package records
import (
 . "goa.design/goa-ai/dsl"
 . "goa.design/goa/v3/dsl"
)
var ScopeRef = Type("ScopeRef",func(){Attribute("name",String);Required("name")})
var RecordsToolset = Toolset("records",func(){
 Tool("read","Read records.",func(){
  Args(func(){Attribute("scope",ScopeRef);Attribute("query",String);Required("scope","query")})
  Return(String)
 })
})
`

const publishedInputConsumer = `package design
import (
 records "contracts.example/records"
 . "goa.design/goa-ai/dsl"
 . "goa.design/goa/v3/dsl"
)
var _ = API("reader",func(){})
var _ = Service("reader",func(){
 Agent("bound","Read records in accepted scope.",func(){Use(records.RecordsToolset,func(){BindInput("read","scope")})})
})
`

const publishedInputBehavior = `package inputbinding
import (
 "testing"
 genrecords "consumer.example/app/gen/reader/toolsets/records"
)
func TestPublishedContractBinding(t *testing.T){
 original:=genrecords.SpecRead()
 bound,err:=prepareReadInput(original,&genrecords.ScopeRef{Name:"accepted"}); if err!=nil {t.Fatal(err)}
 if _,err:=original.Payload.Codec.FromJSON([]byte("{\"query\":\"active\"}"));err==nil {t.Fatal("provider requirement lost")}
 decoded,err:=bound.Payload.Codec.FromJSON([]byte("{\"query\":\"active\"}"));if err!=nil {t.Fatal(err)}
 complete,err:=bound.PrepareInput(decoded);if err!=nil {t.Fatal(err)}
 full,err:=genrecords.ReadPayloadCodec().FromJSON(complete);if err!=nil {t.Fatal(err)}
 if full.Scope.Name!="accepted" || full.Query!="active" {t.Fatalf("incorrect complete input: %#v",full)}
 if _,err:=bound.Payload.Codec.FromJSON([]byte("{\"scope\":{\"name\":\"forged\"},\"query\":\"active\"}"));err==nil {t.Fatal("model can set bound scope")}
}
`
