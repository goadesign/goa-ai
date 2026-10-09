// Package design declares synthetic prompt producers for the independent MCP
// referee. Ordinary Goa unions and strings remain the service contract; the MCP
// plugin owns their conversion to flat protocol messages and suggestions.
package design

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// RefereePromptText declares text instructions returned by the fixture.
var RefereePromptText = Type("RefereePromptText", func() {
	Attribute("text", String, "Message text")
	Required("text")
})

// RefereePromptImage declares image bytes and their media type.
var RefereePromptImage = Type("RefereePromptImage", func() {
	Attribute("data", Bytes, "Encoded synthetic PNG bytes")
	Attribute("mimeType", String, "Image media type")
	Required("mimeType")
})

// RefereeEmbeddedText declares a resource URI and its text contents.
var RefereeEmbeddedText = Type("RefereeEmbeddedText", func() {
	Attribute("uri", String, "Embedded resource identifier", func() { Format(FormatURI) })
	Attribute("text", String, "Embedded resource contents")
	Required("uri", "text")
})

// RefereePromptResource declares the selected embedded resource representation.
var RefereePromptResource = Type("RefereePromptResource", func() {
	OneOf("resource", "Embedded resource representation", func() {
		Attribute("text", RefereeEmbeddedText, "Text resource")
	})
	Required("resource")
})

// RefereePromptMessage declares one author and one selected content block.
var RefereePromptMessage = Type("RefereePromptMessage", func() {
	Attribute("role", String, "Message author", func() { Enum("user") })
	OneOf("content", "Selected message content", func() {
		Attribute("text", RefereePromptText, "Text instructions")
		Attribute("image", RefereePromptImage, "Synthetic image")
		Attribute("resource", RefereePromptResource, "Embedded text resource")
	})
	Required("role", "content")
})

// RefereePromptResult declares the ordered messages returned by a prompt.
var RefereePromptResult = Type("RefereePromptResult", func() {
	Attribute("messages", ArrayOfRequired(RefereePromptMessage), "Ordered prompt messages", func() { MinLength(1) })
	Required("messages")
})

// refereePrompts binds each producer to the prompt selected by the frozen
// scenarios. The completion method supplies suggestions without executing one.
func refereePrompts() {
	Method("simple_prompt", func() {
		Description("Return synthetic text instructions for a selected prompt")
		Result(RefereePromptResult)
		Prompt("test_simple_prompt", "Synthetic text instructions")
	})
	Method("argument_prompt", func() {
		Description("Return instructions containing the two supplied string arguments")
		Payload(func() {
			Attribute("arg1", String, "First prompt argument")
			Attribute("arg2", String, "Second prompt argument")
			Required("arg1", "arg2")
		})
		Result(RefereePromptResult)
		Prompt("test_prompt_with_arguments", "Synthetic parameterized instructions")
	})
	Method("resource_prompt", func() {
		Description("Return instructions containing a synthetic embedded resource")
		Payload(func() {
			Attribute("resourceUri", String, "Requested embedded resource identifier", func() { Format(FormatURI) })
			Required("resourceUri")
		})
		Result(RefereePromptResult)
		Prompt("test_prompt_with_embedded_resource", "Synthetic resource instructions")
	})
	Method("image_prompt", func() {
		Description("Return synthetic image bytes followed by text instructions")
		Result(RefereePromptResult)
		Prompt("test_prompt_with_image", "Synthetic image instructions")
	})
	Method("suggest_argument", func() {
		Description("Return synthetic suggestions for the partial first prompt argument")
		Payload(func() {
			Attribute("value", String, "Partial argument text")
			Attribute("arguments", MapOf(String, String), "Prior prompt argument values")
			Required("value")
		})
		Result(func() {
			Attribute("values", ArrayOf(String), "Suggestions in relevance order", func() { MaxLength(100) })
			Attribute("total", Int64, "All available suggestions")
			Attribute("hasMore", Boolean, "Whether further suggestions exist")
		})
		PromptCompletion("test_prompt_with_arguments", "arg1")
		ResourceCompletion("test://template/{id}/data", "id")
		ResourceCompletion("test://reserved/{+path}{?fields*}", "path")
	})
}
