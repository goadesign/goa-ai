# A host that loads Skills and asks before execution

This reference uses generated MCP clients for discovery and file reads, and a
generated native Goa tool for local execution. `design.go` describes both sides.
`host.go` owns the model context, retained entries, verified byte cache and consent.
`host_test.go` exercises the complete path through HTTP and agent continuation.

Run the generated acceptance fixture from the repository root:

```sh
go test ./codegen/mcp -run '^TestMCPSkillHostHTTP$' -count=1
```

The application builds authenticated clients and assigns connection labels. The
host checks declared resource and Skill support before discovery. Listing and
direct lookup retain complete entries without fetching files. A trusted UI shows
the originating connection, exact Skill URI and complete file manifest before
calling `approveLoad`. Loading fetches only the entry file and verifies its size,
digest and complete YAML frontmatter with `mcp.VerifySkillFile`.

Skill instructions enter ordinary user context with their origin identified. They
do not grant tools or become system instructions. Supporting reads require exact
membership in the retained manifest, including when the file is cached. Reading
a nested `SKILL.md` does not activate it; activation needs its own discovery and
consent. Host labels separate servers even when they serve identical URIs.

The application decides which tools execute local code. This example declares
that tool with `Confirmation` and calls it through its generated `BindTo`
executor. The runtime suspends before execution. The trusted UI calls
`approveExecution` for the pending tool-call ID before sending an affirmative
continuation. Approval covers the complete retained manifest; a changed digest,
added file or removed file revokes it. Denial returns the declared `Denied`
result without reading the script. The supplied execution dependency receives
only verified script bytes. Arbitrary script execution uses one activity attempt
because replay after an uncertain outcome may repeat side effects.

This example declines dynamic entries. Its inclusive limits are 512 files and
16 MiB of raw bytes per Skill, protecting its retained in-memory file contents.
Each Skill gets its own allowance. The shared verifier imposes neither limit.

One host owns one process-local model context and keeps the full entry for every
activated Skill as long as that context exists. A changed version needs a new
context and fresh consent. There is no disk cache or restart-persistent approval.
An application that persists model context must also persist its retained entries
and enforce its own consent lifetime; saving messages alone is insufficient.
