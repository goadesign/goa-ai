// These checks send authored extension objects through the ordinary resource
// endpoint. Generated codecs retain mapped fields and reject invalid metadata
// without requiring service implementations to encode JSON themselves.
package codegen

import (
	"strings"
	"testing"
)

func TestMCPTypedResourceMetadata(t *testing.T) {
	design, runtime := typedResourceMetadataPeer()
	runMCPPeer(t, "resource-peer.local", design, runtime)
}

func TestMCPTypedResourceMetadataView(t *testing.T) {
	design, runtime := typedResourceMetadataPeer()
	design = strings.Replace(design, `var _=Service("records",func(){`, `
var readerPage=ResultType("application/vnd.resource-page",func(){
 TypeName("TypedResources")
 Field(1,"contents",ArrayOfRequired(item),"Ordered resource contents")
 View("default",func(){Attribute("contents")})
 View("page",func(){Attribute("contents")})
})
var _=Service("records",func(){`, 1)
	design = strings.Replace(design, `Result(func(){Field(1,"contents",ArrayOfRequired(item),"Ordered contents; an existing resource may be empty")})`, `Result(readerPage,func(){View("page")})`, 1)
	runtime = strings.ReplaceAll(runtime, "genservice.ReadResult", "genservice.TypedResources")
	runMCPPeer(t, "resource-peer.local", design, runtime)
}

func TestMCPTypedResourceCatalogMetadata(t *testing.T) {
	design, runtime := typedResourceCatalogMetadataPeer()
	runMCPPeer(t, "resource-peer.local", design, runtime)
}

func TestMCPTypedResourceCatalogMetadataViews(t *testing.T) {
	design, runtime := typedResourceCatalogMetadataPeer()
	design, runtime = resourceCatalogViews(design, runtime)
	runMCPPeer(t, "resource-peer.local", design, runtime)
}

func TestMCPTypedPromptMetadata(t *testing.T) {
	design, runtime := typedPromptMetadataPeer()
	runMCPPeer(t, "input-peer.local", design, runtime)
}

func TestMCPInlinePromptMetadata(t *testing.T) {
	design, runtime := typedPromptMetadataPeer()
	design = strings.Replace(design, `Field(2,"_meta",resourceMetadata,"Authored extension fields",func(){`, `Field(2,"_meta",func(){Description("Authored extension fields");`+inlineMetadataFields, 1)
	runtime = strings.ReplaceAll(runtime, "genrecords.ResourceMetadata", "struct{Policy *genrecords.UIPolicy;Note string}")
	runMCPPeer(t, "input-peer.local", design, runtime)
}

// typedPromptMetadataPeer supplies one ordinary authored prompt with metadata.
// Named and inline object cases use the same authorization and content checks.
func typedPromptMetadataPeer() (string, string) {
	design := strings.Replace(inputExchangeDesign, `var promptText=`, typedResourceMetadataDesign+`var promptText=`, 1)
	design = strings.Replace(design, `Field(1,"text",String,"Instructions shown to the user");`, `Field(1,"text",String,"Instructions shown to the user");Field(2,"_meta",resourceMetadata,"Authored extension fields",func(){Meta("struct:field:name","Metadata")});`, 1)
	runtime := strings.Replace(inputExchangeRuntime, `Text:string(label)`, `Text:string(label),Metadata:&genrecords.ResourceMetadata{Policy:&genrecords.UIPolicy{PrefersBorder:new(true)},Note:"checked"}`, 1)
	runtime = strings.ReplaceAll(runtime, `require.Len(t,instructions.Messages,1);`, `require.Len(t,instructions.Messages,1);assert.JSONEq(t,"{\"ui\":{\"prefersBorder\":true},\"note\":\"checked\"}",string(instructions.Messages[0].Content.Meta));`)
	return design, runtime
}

func TestMCPTypedTaskContentMetadata(t *testing.T) {
	design, runtime := taskContentPeer(t)
	design = strings.Replace(design, `var text=`, typedResourceMetadataDesign+`var text=`, 1)
	design = strings.Replace(design, `Required("summary")`, `Field(3,"receipt",text,"Domain field reusing the presentation type");Required("summary")`, 1)
	design = strings.Replace(design, `Field(1,"text",String,"Presentation text");`, `Field(1,"text",String,"Presentation text");Field(2,"_meta",resourceMetadata,"Authored extension fields",func(){Meta("struct:field:name","Metadata")});`, 1)
	runtime = strings.Replace(runtime, `Text:"user presentation"`, `Text:"user presentation",Metadata:&genjobs.ResourceMetadata{Policy:&genjobs.UIPolicy{PrefersBorder:new(true)},Note:"checked"}`, 1)
	runtime = strings.Replace(runtime, `assert.Equal(t,"user presentation",presentation.Text)`, `assert.Equal(t,"user presentation",presentation.Text);assert.JSONEq(t,"{\"ui\":{\"prefersBorder\":true},\"note\":\"checked\"}",string(presentation.Meta))`, 1)
	runMCPPeer(t, "task-peer.local", design, runtime)
}

func TestMCPTypedTaskResultMetadata(t *testing.T) {
	design, runtime := typedTaskResultMetadataPeer(t)
	runMCPPeer(t, "task-peer.local", design, runtime)
}

func TestMCPInlineTaskResultMetadata(t *testing.T) {
	design, runtime := typedTaskResultMetadataPeer(t)
	design = strings.Replace(design, `Field(3,"hostData",resourceMetadata,"Private app result",func(){`, `Field(3,"hostData",func(){Description("Private app result");`+inlineMetadataFields, 1)
	runtime = strings.ReplaceAll(runtime, "genjobs.ResourceMetadata", "struct{Policy *genjobs.UIPolicy;Note string}")
	runMCPPeer(t, "task-peer.local", design, runtime)
}

// typedTaskResultMetadataPeer follows native job completion through tasks/get.
// Both object representations retain the same authored result metadata.
func typedTaskResultMetadataPeer(t *testing.T) (string, string) {
	t.Helper()
	design, runtime := taskContentPeer(t)
	design = strings.Replace(design, `var text=`, typedResourceMetadataDesign+`var text=`, 1)
	design = strings.Replace(design, `Required("summary")`, `Field(3,"hostData",resourceMetadata,"Private app result",func(){Meta("struct:field:name","PrivateInfo")});Required("summary")`, 1)
	design = strings.Replace(design, `ToolContent("attachments")`, `ToolContent("attachments");ToolMetadata("hostData")`, 1)
	runtime = strings.Replace(runtime, `Summary:accepted.Content.Label,`, `Summary:accepted.Content.Label,PrivateInfo:&genjobs.ResourceMetadata{Policy:&genjobs.UIPolicy{PrefersBorder:new(true)},Note:"checked"},`, 1)
	assertion := `assert.Equal(t,"user presentation",presentation.Text)`
	runtime = strings.Replace(runtime, assertion, assertion+`
 metadataLocation,metadataErr:=url.Parse(peer.URL);require.NoError(t,metadataErr)
 metadataClient:=genclient.NewClient(metadataLocation.Scheme,metadataLocation.Host,httpClient,goahttp.RequestEncoder,goahttp.ResponseDecoder,false)
 metadataClient.Doer=transport
 metadataReply,metadataErr:=metadataClient.TasksGet()(t.Context(),&genmcp.TasksGetPayload{TaskID:id,HTTPPath0:"owner"});require.NoError(t,metadataErr)
 metadataTask,metadataOK:=metadataReply.(*genmcp.TasksGetResult).Outcome.AsCompleted();require.True(t,metadataOK)
 assert.JSONEq(t,"{\"ui\":{\"prefersBorder\":true},\"note\":\"checked\",\"io.modelcontextprotocol/serverInfo\":{\"name\":\"jobs\",\"version\":\"1\"}}",string(metadataTask.Result.Meta))
 assert.NotContains(t,string(metadataTask.Result.StructuredContent),"hostData")
 assert.NotContains(t,string(metadataTask.Result.StructuredContent),"checked")`, 1)
	return design, runtime
}

// typedResourceCatalogMetadataPeer uses the same authored metadata in both
// descriptor catalogs, including omitted values and invalid field validation.
func typedResourceCatalogMetadataPeer() (string, string) {
	design, runtime := resourceCatalogPeer()
	design = strings.Replace(design, `var text=Type("ResourceText",func(){`, typedResourceMetadataDesign+`var text=Type("ResourceText",func(){`, 1)
	design = strings.Replace(design, `Field(7,"_meta",Any,"Extension object",func(){Meta("struct:field:type","json.RawMessage","encoding/json")})`, `Field(7,"_meta",resourceMetadata,"Authored extension object",func(){Meta("struct:field:name","Metadata")})`, 1)
	runtime = strings.Replace(runtime, `Meta:[]byte("{\"id\":9007199254740993}")`, `Metadata:&genservice.ResourceMetadata{Policy:&genservice.UIPolicy{PrefersBorder:new(true)},Note:"checked"}`, 1)
	runtime = strings.Replace(runtime, `entry.Meta=[]byte("null")`, `entry.Metadata.Note="invalid"`, 1)
	runtime = strings.Replace(runtime, `"{\"id\":9007199254740993}"`, `"{\"ui\":{\"prefersBorder\":true},\"note\":\"checked\"}"`, 1)
	design = strings.Replace(design, `Field(3,"title",String,"Template display name")`, `Field(3,"title",String,"Template display name")
 Field(4,"_meta",resourceMetadata,"Authored extension fields",func(){Meta("struct:field:name","Metadata")})`, 1)
	runtime = strings.Replace(runtime, `entry.Label="blob"`, `entry.Label="blob";entry.Metadata=nil`, 1)
	runtime = strings.Replace(runtime, `assert.Nil(t,page.NextCursor)`, `assert.Nil(t,page.NextCursor);assert.Empty(t,page.Resources[0].Meta)`, 1)
	runtime = strings.Replace(runtime, `Title:new("Records")`, `Title:new("Records"),Metadata:&genservice.ResourceMetadata{Policy:&genservice.UIPolicy{PrefersBorder:new(true)},Note:"checked"}`, 1)
	runtime = strings.Replace(runtime, `assert.Len(t,templates.(*genmcp.ResourceTemplatesListResult).ResourceTemplates,1)`, `require.Len(t,templates.(*genmcp.ResourceTemplatesListResult).ResourceTemplates,1);assert.JSONEq(t,"{\"ui\":{\"prefersBorder\":true},\"note\":\"checked\"}",string(templates.(*genmcp.ResourceTemplatesListResult).ResourceTemplates[0].Meta))`, 1)
	return design, runtime
}

// typedResourceMetadataPeer adds a typed policy to the existing authenticated
// reader fixture while preserving its mapped URL, credentials and middleware.
func typedResourceMetadataPeer() (string, string) {
	design := strings.Replace(resourceCatalogPeerDesign, `var text=Type("ResourceText",func(){`, typedResourceMetadataDesign+`var text=Type("ResourceText",func(){`, 1)
	design = strings.Replace(design, ` Field(2,"text",String,"Resource text, including an empty string")`, ` Field(2,"text",String,"Resource text, including an empty string")
 Field(3,"_meta",resourceMetadata,"Authored extension fields",func(){Meta("struct:field:name","Metadata")})`, 1)
	runtime := resourceCatalogPeerRuntime
	runtime = strings.Replace(runtime, `type resourceService struct{reads,fixed int;lastURI string}`, `type resourceService struct{reads,fixed int;lastURI string;invalidMetadata,omitMetadata bool}`, 1)
	runtime = strings.Replace(runtime, `URI:p.Address,Text:""`, `URI:p.Address,Text:"",Metadata:s.metadata()`, 1)
	runtime = strings.Replace(runtime, `assert.Empty(t,*result.Contents[0].Text);`, `assert.Empty(t,*result.Contents[0].Text);assert.JSONEq(t,"{\"ui\":{\"prefersBorder\":true,\"csp\":{\"connectDomains\":[\"https://data.example\"]}},\"note\":\"checked\"}",string(result.Contents[0].Meta));`, 1)
	runtime = strings.Replace(runtime, ` _,err=client.ResourcesRead()`, ` s.omitMetadata=true
 omitted,omittedErr:=client.ResourcesRead()(t.Context(),&genmcp.ResourcesReadPayload{HTTPPath0:"blue",URI:"test://records/text?view=exact"});require.NoError(t,omittedErr)
 omittedComplete,ok:=omitted.(*genmcp.ResourcesReadResult).Outcome.AsComplete();require.True(t,ok);assert.Empty(t,omittedComplete.Contents[0].Meta)
 s.omitMetadata=false
 s.invalidMetadata=true
 _,invalidErr:=client.ResourcesRead()(t.Context(),&genmcp.ResourcesReadPayload{HTTPPath0:"blue",URI:"test://records/text?view=exact"});require.Error(t,invalidErr)
 s.invalidMetadata=false
 _,err=client.ResourcesRead()`, 1)
	runtime += `
func(s *resourceService)metadata()*genservice.ResourceMetadata {
 if s.omitMetadata{return nil}
 note:="checked";if s.invalidMetadata{note="invalid"}
 return &genservice.ResourceMetadata{Policy:&genservice.UIPolicy{PrefersBorder:new(true),Csp:&genservice.UICSP{ConnectDomains:[]string{"https://data.example"}}},Note:note}
}
`
	return design, runtime
}

const inlineMetadataFields = `Field(1,"ui",uiPolicy,"Browser policy for this content",func(){Meta("struct:field:name","Policy")});Field(2,"note",String,"Validated extension value",func(){Enum("checked")});Required("ui","note");`

const typedResourceMetadataDesign = `
var uiCSP=Type("UICSP",func(){Field(1,"connectDomains",ArrayOfRequired(String),"Allowed network origins")})
var uiPolicy=Type("UIPolicy",func(){
 Field(1,"prefersBorder",Boolean,"Whether the host should show a border")
 Field(2,"csp",uiCSP,"Allowed browser resource origins")
})
var resourceMetadata=Type("ResourceMetadata",func(){
 Field(1,"ui",uiPolicy,"Browser policy for this resource",func(){Meta("struct:field:name","Policy")})
 Field(2,"note",String,"Synthetic validated extension value",func(){Enum("checked")})
 Required("ui","note")
})
`
