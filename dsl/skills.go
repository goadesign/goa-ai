// Package dsl binds skill discovery to ordinary Goa methods. The application
// owns the files and access checks; the MCP host owns verification and activation.
package dsl

import "goa.design/goa/v3/eval"

// SkillCatalog selects the current unary method to list complete skill entries.
// Its payload contains only optional cursor apart from native HTTP inputs. Its
// result contains an optional skills ArrayOfRequired and optional nextCursor.
// Each entry declares required uri, frontmatter and resources. Frontmatter uses
// json.RawMessage to preserve every authored YAML field; resources is an untagged
// OneOf whose manifest branch is a file array and dynamic branch is the literal
// string "dynamic". Each file declares required uri, digest and size.
// SkillLookup and ResourceReader must
// also be declared. Listing returns metadata only and never activates a skill.
func SkillCatalog() {
	method, server := mcpMethod()
	if method == nil {
		return
	}
	if server.SkillCatalog != nil {
		eval.ReportError("an MCP service can declare only one skill catalog")
		return
	}
	server.SkillCatalog = method
}

// SkillLookup selects the current unary method to look up one complete skill
// entry by its required uri string, apart from native HTTP inputs. Its result
// contains only a required skill object with the same contract as SkillCatalog
// entries. The method must serve known skill URIs even when the catalog omits
// them. ResourceReader supplies files; lookup does not read or activate them.
func SkillLookup() {
	method, server := mcpMethod()
	if method == nil {
		return
	}
	if server.SkillLookup != nil {
		eval.ReportError("an MCP service can declare only one skill lookup")
		return
	}
	server.SkillLookup = method
}
