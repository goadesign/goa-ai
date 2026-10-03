// This file compiles a resource-only service in a separate module. Located
// aliases and unions must retain their declared types without depending on
// prompt or tool generation to supply imports or conversion functions.
package codegen

import "goa.design/goa/v3/expr"

// resourceReaderFixture declares a located URI alias and selected text contents
// for the resource-only compiled module.
func resourceReaderFixture(method *expr.MethodExpr) []expr.UserType {
	uri := promptFixtureType("ReaderURI", expr.String)
	uri.Meta = expr.MetaExpr{"struct:pkg:path": {"resource/shared"}}
	text := promptFixtureType("ReaderText", &expr.Object{
		{Name: "uri", Attribute: &expr.AttributeExpr{Type: uri, Validation: &expr.ValidationExpr{Format: expr.FormatURI}}},
		{Name: "text", Attribute: &expr.AttributeExpr{Type: expr.String}},
	}, "uri", "text")
	choice := promptFixtureType("ReaderContent", &expr.Union{TypeName: "ReaderChoice", Values: []*expr.NamedAttributeExpr{
		{Name: "text", Attribute: &expr.AttributeExpr{Type: text}},
	}})
	choice.Meta = expr.MetaExpr{"struct:pkg:path": {"resource/shared"}}
	item := promptFixtureType("ReaderItem", &expr.Object{{Name: "content", Attribute: &expr.AttributeExpr{Type: choice}}}, "content")
	result := promptFixtureType("ReaderResult", &expr.Object{{Name: "contents", Attribute: &expr.AttributeExpr{
		Type: &expr.Array{ElemType: &expr.AttributeExpr{Type: item}, NonNullableElems: true},
	}}})
	method.Payload = &expr.AttributeExpr{Type: &expr.Object{{Name: "uri", Attribute: &expr.AttributeExpr{Type: uri}}}, Validation: &expr.ValidationExpr{Required: []string{"uri"}}}
	method.Result = &expr.AttributeExpr{Type: result}
	return []expr.UserType{uri, text, choice, item, result}
}

const resourceReaderGeneratedTestSource = `package client

import (
 "context"
 "net/http/httptest"
 "net/url"
 "testing"
 genreader "generated.local/gen/templated"
 genresourceshared "generated.local/gen/resource/shared"
 genmcp "generated.local/gen/mcp_templated"
 genserver "generated.local/gen/jsonrpc/mcp_templated/server"
 goahttp "goa.design/goa/v3/http"
)

type resourceService struct {address string; calls int; result *genreader.ReaderResult}
func(s *resourceService)Read(_ context.Context,p *genreader.ReadPayload)(*genreader.ReaderResult,error) {
 s.calls++;s.address=string(p.URI);return s.result,nil
}
func TestResourceOnlyReader(t *testing.T) {
 content:=genresourceshared.ReaderContent(genresourceshared.NewReaderChoiceText(&genresourceshared.ReaderText{URI:genresourceshared.ReaderURI("test://items/abc"),Text:""}))
 service:=&resourceService{result:&genreader.ReaderResult{Contents:[]*genreader.ReaderItem{{Content:content}}}}
 mux:=goahttp.NewMuxer()
 adapter:=genmcp.NewMCPAdapter(service,nil)
 server:=genserver.New(genmcp.NewEndpoints(adapter),mux,goahttp.RequestDecoder,goahttp.ResponseEncoder,nil)
 genserver.Mount(mux,server)
 endpoint:=httptest.NewServer(mux);defer endpoint.Close()
 location,err:=url.Parse(endpoint.URL);if err!=nil{t.Fatal(err)}
 client:=NewClient(location.Scheme,location.Host,endpoint.Client(),goahttp.RequestEncoder,goahttp.ResponseDecoder,false)
 catalog,err:=client.ResourcesTemplatesList()(t.Context(),&genmcp.ResourceTemplatesListPayload{});if err!=nil{t.Fatal(err)}
 if len(catalog.(*genmcp.ResourceTemplatesListResult).ResourceTemplates)!=1{t.Fatal("missing template")}
 fixed,err:=client.ResourcesList()(t.Context(),&genmcp.ResourcesListPayload{});if err!=nil{t.Fatal(err)}
 if len(fixed.(*genmcp.ResourcesListResult).Resources)!=0{t.Fatal("invented fixed resource")}
 got,err:=client.ResourcesRead()(t.Context(),&genmcp.ResourcesReadPayload{URI:"test://items/abc"});if err!=nil{t.Fatal(err)}
 result:=got.(*genmcp.ResourcesReadResult)
 if service.calls!=1||service.address!="test://items/abc"||len(result.Contents)!=1||result.Contents[0].Text==nil||*result.Contents[0].Text!=""{t.Fatalf("result=%+v service=%+v",result,service)}
 service.result=&genreader.ReaderResult{}
 empty,err:=client.ResourcesRead()(t.Context(),&genmcp.ResourcesReadPayload{URI:"test://items/empty"});if err!=nil{t.Fatal(err)}
 if len(empty.(*genmcp.ResourcesReadResult).Contents)!=0{t.Fatal("empty resource gained content")}
}
`
