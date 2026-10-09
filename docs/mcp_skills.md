# Serve Skills over MCP

The MCP Skills extension exposes workflow instructions and supporting files
through an existing MCP service. `SkillCatalog()` binds an ordinary Goa method
to `skills/list`; `SkillLookup()` binds another method to `skills/get`.
`ResourceReader()` supplies files through `resources/read`. Your service owns
visibility, authorization, pagination and the exact bytes it serves.

Declare all three methods together. Generation advertises
`io.modelcontextprotocol/skills` only when both discovery methods are present.
Add `ResourceDirectory()` when the service also owns directory pages. Generation
advertises `directoryRead: true` only when that method is declared.

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

## List directory children

Declare `ResourceDirectory()` on an ordinary unary method. Apart from native HTTP
inputs, its payload has required `uri` and optional `cursor` strings. Its result
has an optional `resources` collection declared with `ArrayOfRequired`, plus
optional `nextCursor`. Each child uses the ordinary resource descriptor shape:
required `uri` and `name`, with optional media type, description, icons,
annotations and authored metadata.

Directory URIs have no trailing slash. Return files and subdirectories one level
below the requested URI. Subdirectories declare `mimeType: "inode/directory"`.
An empty directory returns an empty protocol array. Pagination works like
`resources/list`; the client returns `nextCursor` as the following request's
`cursor`. Serve every directory in the Skill namespaces your service exposes.

The generated adapter rejects malformed directory requests with `-32602` and
invalid server pages with `-32603`. Declare and return `invalid_params` when a
resource is unknown or is not a directory. Native authentication, mapped route
inputs and selected result views keep their ordinary Goa behavior.

A directory page describes the server's current contents. It does not change
the complete manifest held by a host acting on a Skill. Newly listed files require
a refreshed entry and any renewed content-bound approval before use.

## Loading belongs to the host

Discovery returns metadata and performs no resource reads or activation.
Content loading must retain the originating server identity and complete entry,
verify raw file bytes against the retained size and digest, compare all YAML
frontmatter fields, and obtain any required approval before use. A read does
not grant tool permissions or activate a nested skill.

Use the generated protocol client for discovery and ordinary resource reads.
After fetching a file, call `mcp.VerifySkillFile(ctx, retainedEntryJSON, uri, bytes)`.
The verifier decodes the complete retained entry, checks exact file membership,
size and SHA-256, and compares every YAML field when reading the entry's own
`SKILL.md`. It accepts original LF or CRLF bytes, preserves exact numbers, and
rejects invalid bytes or frontmatter. A nested entry remains supporting content
until separately discovered and approved. Dynamic entries have no stable
manifest and cannot pass this verification operation.

The [reference host](../codegen/mcp/testdata/skills_host/README.md) composes these
clients with existing agent tool confirmation and a native `BindTo` executor.
It assigns server labels in the application, retains complete entries alongside
ordinary user context, and reads files lazily after consent. Its cache separates
origins and digests and verifies each cached file against the entry being used.
Manifest changes revoke consent; frontmatter such as `allowed-tools` grants no
permissions. Local code execution needs explicit consent for that exact Skill
and complete manifest. Denial reads no script and executes nothing.

Consent and model context belong to the host application. This reference keeps
both in memory and requires a fresh context and approval for a changed version
or a restarted host. Applications that persist context must retain its entries
with it and define their consent lifetime. The shared verifier makes no storage
or approval decisions. The complete upgrade still requires the
[remaining completion gates](mcp_protocol_upgrade_plan.md#current-completion-gates).

The authoritative contracts are the [MCP Skills extension](https://modelcontextprotocol.io/extensions/skills/overview)
and [Agent Skills format](https://agentskills.io/specification).
