// Package codegen combines each query and page hint under the public query name.
// Generated payload-type checks select the authored branch before its template
// reads arguments, so page requests never enter the initial query's template.
package codegen

import "fmt"

// callHintTemplate uses the tool's final generated name to select the query or
// page text. Ordinary tools keep their authored template unchanged.
func callHintTemplate(tool *ToolData) string {
	if tool.Page == nil {
		return tool.CallHintTemplate
	}
	return pagingHintTemplate(tool.ConstName, tool.CallHintTemplate, tool.Page.CallHintTemplate, ".")
}

// resultHintTemplate selects a paged tool's result text from its typed Args.
// Ordinary tools keep their authored result template unchanged.
func resultHintTemplate(tool *ToolData) string {
	if tool.Page == nil {
		return tool.ResultHintTemplate
	}
	return pagingHintTemplate(tool.ConstName, tool.ResultHintTemplate, tool.Page.ResultHintTemplate, ".Args")
}

// pagingHintTemplate combines the query and page text with their generated type
// selector. A branch without an authored template reports no hint to its caller.
func pagingHintTemplate(name, query, page, payload string) string {
	if query == "" && page == "" {
		return ""
	}
	if query == "" {
		query = "{{noHint}}"
	}
	if page == "" {
		page = "{{noHint}}"
	}
	return fmt.Sprintf("{{if isPage%s %s}}%s{{else}}%s{{end}}", name, payload, page, query)
}
