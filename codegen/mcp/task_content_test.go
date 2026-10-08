// This fixture combines typed presentation content, a read-selected view and
// host input before job creation. Task results must use the read owner's view
// and keep presentation fields out of the structured domain result.
package codegen

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMCPGeneratedTaskContentAfterCreationInput(t *testing.T) {
	design, runtime := taskPeerCreationInput(taskOwnerDesign, taskOwnerRuntime)
	design, runtime = taskPeerViews(design, runtime)
	design = strings.Replace(design, `var observation=`, `
var text=Type("Text",func(){Field(1,"text",String,"Presentation text");Required("text")})
var attachment=Type("Attachment",func(){OneOf("content","Presentation item",func(){TypeName("Presentation");Attribute("text",text,"Text for the user")});Required("content")})
var output=Type("JobOutput",func(){Field(1,"summary",String,"Structured domain result");Field(2,"attachments",ArrayOfRequired(attachment),"Presentation content");Required("summary")})
var observation=`, 1)
	design = strings.Replace(design, `Attribute("complete",String,"Finished label")`, `Attribute("complete",output,"Finished domain result with presentation content")`, 1)
	design = strings.Replace(design, `Tool("create","Create a durable synthetic job")`, `Tool("create","Create a durable synthetic job",func(){ToolContent("attachments")})`, 1)
	runtime = strings.Replace(runtime, `mcpruntime "goa.design/goa-ai/runtime/mcp"`, `mcpruntime "goa.design/goa-ai/runtime/mcp"
 "goa.design/goa-ai/runtime/content"`, 1)
	runtime = strings.Replace(runtime, `genjobs.JobStateBranchComplete(accepted.Content.Label)`, `&genjobs.JobOutput{Summary:accepted.Content.Label,Attachments:[]*genjobs.Attachment{{Content:genjobs.NewPresentationText(&genjobs.Text{Text:"user presentation"})}}}`, 1)
	assertion := `assert.JSONEq(t,"{\"type\":\"alternate\",\"value\":\"done\"}",string(finished.StructuredContent))`
	require.Contains(t, runtime, assertion)
	runtime = strings.Replace(runtime, assertion, `assert.JSONEq(t,"{\"type\":\"alternate\",\"value\":{\"summary\":\"done\"}}",string(finished.StructuredContent));require.Len(t,finished.Content,1);presentation,ok:=finished.Content[0].(*content.TextContent);require.True(t,ok);assert.Equal(t,"user presentation",presentation.Text)`, 1)
	runTaskPeer(t, design, runtime)
}
