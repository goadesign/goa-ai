# Serve Skills over MCP

The MCP Skills extension exposes workflow instructions and supporting files
through an existing MCP service. `SkillCatalog()` binds an ordinary Goa method
to `skills/list`; `SkillLookup()` binds another method to `skills/get`.
`ResourceReader()` supplies files through `resources/read`. Your service owns
visibility, authorization, pagination and the exact bytes it serves.

Declare all three methods together. Generation advertises
`io.modelcontextprotocol/skills` only when both discovery methods are present.
Directory reading is not advertised by these declarations.

## Declare complete entries

A catalog payload has an optional `cursor` string. Its result has an optional
`skills` collection declared with `ArrayOfRequired`, plus an optional
`nextCursor`. An absent collection becomes an empty protocol page. A lookup
payload has a required `uri` string; its result has only a required `skill`
object. Native credentials and mapped HTTP path inputs remain ordinary Goa
inputs rather than MCP parameters. Selected result views retain the same entry
contract.

Each entry declares required `uri`, `frontmatter` and `resources` fields:

```go
var SkillFile = Type("SkillFile", func() {
    Field(1, "uri", String, "Full file resource URI", func() { Format(FormatURI) })
    Field(2, "digest", String, "SHA-256 of the served raw bytes", func() {
        Pattern(`^sha256:[0-9a-f]{64}$`)
    })
    Field(3, "size", Int64, "Length of the served raw bytes", func() { Minimum(0) })
    Required("uri", "digest", "size")
})

var SkillEntry = Type("SkillEntry", func() {
    Field(1, "uri", String, "Exact SKILL.md resource URI", func() { Format(FormatURI) })
    Field(2, "frontmatter", Any, "Every authored YAML field", func() {
        Meta("struct:field:type", "json.RawMessage", "encoding/json")
    })
    OneOf("resources", "Complete file manifest or generated content", func() {
        Meta("oneof:json:untagged")
        Attribute("manifest", ArrayOfRequired(SkillFile), "Every skill file")
        Attribute("dynamic", String, "Content without stable digests", func() {
            Enum("dynamic")
        })
    })
    Required("uri", "frontmatter", "resources")
})
```

Frontmatter is an open JSON object because the extension requires every authored
field, including future fields. Preserve exact numbers and values. Do not
replace it with a struct containing only known fields. The generated adapter
checks the known Agent Skills fields and the relationships between the entry,
its directory and its manifest. Invalid server entries produce JSON-RPC
`-32603`; no entry is returned to the client.

A manifest contains every file, including the entry's exact `SKILL.md` URI.
Each URI must remain inside that skill's directory, and a URI may occur only
once. Compute digests and sizes from the bytes your resource reader serves.
The adapter checks the manifest but does not read files to recompute digests.
Use the `dynamic` branch only for content without stable digests.

Serve direct lookups even when a skill is absent from the catalog. Return a
declared Goa `invalid_params` error for an unknown skill or file; the generated
protocol maps it to JSON-RPC `-32602`. Regenerate both peers when adding these
methods. This is a breaking protocol upgrade; mixed revisions are unsupported.

## Loading belongs to the host

Discovery returns metadata and performs no resource reads or activation.
Content loading must retain the originating server identity and complete entry,
verify raw file bytes against the retained size and digest, compare all YAML
frontmatter fields, and obtain any required approval before use. A read does
not grant tool permissions or activate a nested skill.

The verified host-loading integration is still being implemented for this
upgrade. Discovery support alone does not complete that integration or authorize
automatic loading. See the [upgrade completion gates](mcp_protocol_upgrade_plan.md#current-completion-gates).

The authoritative contracts are the [MCP Skills extension](https://modelcontextprotocol.io/extensions/skills/overview)
and [Agent Skills format](https://agentskills.io/specification).
