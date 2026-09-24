# Retained native image sources

A tool can select an image whose bytes are held by its application. The tool
returns its ordinary semantic result and typed evidence `ServerData`. Mark that
evidence with `NativeImage()` to save a self-contained image descriptor in the
conversation. When a concrete model request uses the image, the runtime asks the
application to verify current access and load the exact bytes.

This keeps relevance with the planner. For example, `view_image(id)` can choose an
earlier image after a new image arrives. Neither the runtime nor the reader picks
the newest image, expands a catalog, or substitutes a description of the image.
Unselected resources cause no image reads.

## Declaration and ownership

Inside a tool declaration:

```go
ServerData("images.retained.v1", RetainedImage, func() {
    AudienceEvidence()
    NativeImage()
    FromMethodResultField("source")
})
```

`RetainedImage` is a precise application-owned Goa type. It must contain enough
information for the owner to locate and verify immutable image contents without
looking up the original tool result. Identity, format, byte length and checksum
can belong to that type when the owning service requires them. The framework
adds none of those domain fields. The model chooses the semantic identifier in
the tool's ordinary arguments; it does not copy this private descriptor.
Each emitted marked item describes one image and becomes one `ImagePart`.

Generation emits the marker in `tools.ServerDataSpec` and the portable registry
consumer contract. It also emits `NativeImageSources()` in marked toolset spec
packages. This factory returns only the producer identifiers and their marked
source schemas, field metadata, and codecs. It does not construct a catalog or
register executable tools.

Pass those declarations to `runtime.WithImageSourceResolver` when constructing
the runtime. The option takes the generated producer-to-source map and a function:

```go
func(context.Context, run.Context, model.ImageSourcePart, int64) (model.ImagePart, error)
```

The final argument is the remaining byte allowance for `Format` plus `Bytes` in
this exact candidate. The reader must:

1. Decode `Data` with the generated codec for `SourceKind`.
2. Verify the current run's authorization and exact resource membership on every
   use. A previous successful read grants no later access.
3. Check the descriptor's known byte length against the allowance **before**
   external I/O. Return `model.ErrImageSourceCapacity` only for proven local
   candidate byte shortage.
4. Read within the inherited finite deadline and allowance. Verify the returned
   type, length and immutable contents against the descriptor, then return the
   existing `ImagePart` with required bytes.

An owning byte-read API may combine current membership and immutable-content
verification. Do not add a separate metadata read merely to fetch a filename or
repeat checks that this API already owns. Conversely, possession of the
descriptor alone is never authorization.

The runtime owns a copy of the admitted declarations. Conflicting contracts for
one source kind, duplicate producer/kind entries, missing codecs, and markers
outside evidence fail at construction. An executable marked producer must match
its admitted declaration at registration and production. Equality compares
declarative metadata, never function addresses; the first admitted kind owns its
decoder. Hosts must supply trusted generated codecs, not unrelated functions
behind identical metadata.

Producer admission compares the full generated declaration, including its Go
type name. Historical kind matching ignores only that top-level producer-specific
Go alias: two tools can emit the same kind and schema through differently named
generated types. Schema, field descriptions and all other declaration metadata
must still match. The first producer in sorted identifier order supplies the
historical codec. Keeping that codec registered does not mark current ordinary
evidence as an image; each new result uses its exact selected tool declaration.

## Saved data and execution

The complete path is:

1. The generated service executor extracts and encodes evidence from the typed
   method result. Semantic result JSON remains unchanged.
2. `ExecuteToolActivity` validates the admitted producer. Existing tool output
   and result records retain their normal generated JSON and evidence.
3. `appendUserToolRecordResults` appends all correlated tool results in record
   order, followed by each result's correlation text and marked images in source
   order. It saves `ImageSourcePart{SourceKind, Data}`, with JSON part kind
   `image_source`. No image is read.
4. Transcript deltas, workflow transport, replay, storage and message cloning
   preserve those descriptor bytes. Historical validation uses registered kind
   codecs and performs no access check or image read.
5. Ordinary request preparations, including cache policy and whole-turn history
   selection, run before final image expansion. Each actual count candidate,
   summary input and final input is then independently resolved.
6. Providers receive only existing `ImagePart` values. `NewRequestContract`
   rejects unresolved source parts. Assistant output rejects source parts too.
   Provider adapters and inference transports do not need a new wire variant.

Published initial history stores descriptors in its ordinary literal records.
PreparedRun references identify that saved history and the compiled start;
preparation recovery does not read image bytes. A suspension checkpoint retains
the exact history position, and continuation reconstructs the descriptors from
that position before a later model request can resolve them.

`model.WithImageSourceResolver` installs the corresponding reader on an opaque
model client. It rejects a second installation, even of the same function.
Wrappers preserve the reader, preparations and observers. Register unbound base
clients and bind fresh current-run and summary clients from those bases.
`CountTokens` does not run ordinary request preparations; it
resolves the actual candidate passed to it. The runtime's history policy uses
its destination base client for counts. The private preparation context carries
the same current-run reader to the separate summary client. It carries no
selected-image list, cached bytes, durable history, or run labels.

Thus summaries see the original pixels of the selected older evidence. Ordinary
history hashes, saved descriptors and copied conversations continue to describe
the original messages. Reading an image never rewrites history. Count, Complete
and Stream preserve the same image occurrences, complete request settings, tool
schemas and ordering when given equivalent prepared input. An estimated token
counter remains `Exact=false`; expansion does not make it exact.

## Candidate and total work

Expansion uses the existing model canonical-request byte and visit checks. Call
that byte allowance `P`. Preflight runs before cloning oversized input or invoking
the reader. All fixed text, native media, metadata, schemas and settings are
charged first. Descriptor charges are replaced by actual format and image bytes.
Repeated occurrences are charged repeatedly. Each reader call gets the remaining
allowance; violating that allowance is a reader contract error, not a capacity
signal.

Reads are serial and uncached within a preparation. A candidate holds at most its
admitted native input; Compress does not retain an expanded list of candidates.
The reader's temporary buffer and the client's owned byte copy can coexist.
Observers and provider SDK encoding can create further copies according to their
existing contracts. `P` is neither total process memory nor an encoded transport,
provider, image-count or pixel limit.

For the actual Compress search, let `N` be its original logical turn count and
`K = min(N, KeepMaxTurns)` when that cap is configured, otherwise `K = N`.
Let `g` be trigger counts (zero or one), `t` be exact-tail counts, `p` be reusable
prior-summary fit counts, `f` be new-summary fit counts, and `z` be summary
requests (zero or one). Then:

| Path | Actual work |
| --- | --- |
| Empty/system-only input | No history count or summary. |
| Trigger not reached | `g`; existing prior summary may supply that candidate. |
| Turn-only retention, no token budgets | No counts; at most one summary. |
| Exact tail can keep the original history | `g + t`, no summary or fit. |
| Prior summary fits | `g + t + p`, no new summary. |
| Prior coverage ends before a fitting candidate | `g + t + p + f`, one new summary. |
| Prior covers all older turns but even newest plus summary fails | Explicit error; no replacement-summary retry. |
| Required newest or summary fails | Stop immediately; no final inference dispatch. |

With a positive total token ceiling and `N >= 2`, count calls are at most `3K`
when `K < N`, or `3N - 2` when `K = N`. These bounds include failed prior reuse
followed by a new summary. The loops test coverage and turn limits before
counting; errors can end them earlier. With only an older-tail token budget,
trigger counts and fit counts are absent and tail counts are at most `K`.
Turn parsing and configured retention determine these bounds; no session-wide
image cap is introduced.

Including a successful final dispatch, there are
`E = g + t + p + f + z + 1` possible materializations. Their exact read work is the
sum of image occurrences and admitted byte lengths in those actual candidates,
not the number of distinct images in history. Successful expanded candidate
bytes are each bounded by `P`; total successful read bytes are at most `E * P`.
The canonical visit allowance bounds occurrences within each candidate. A
cooperative reader bounds its RPC attempts and any incomplete/error read by
that candidate's allowance and inherited deadline; the framework performs no
automatic reader retries. A deadline alone is not the byte-work proof.

`E` bounds framework client invocations, not every HTTP/RPC attempt inside an
adapter. Actual network requests also depend on the adapter's counting method
and configured retry contract. For example, Bedrock Responses estimates locally
and sends no counting HTTP request. Hosts must account for their reader and
provider retries separately; the framework adds no retry loop.

`ErrImageSourceCapacity` may trigger compression or discard an optional older
whole turn through the existing fit search. It cannot remove the newest required
turn or cause a summary to silently omit pixels. Authorization, changed checksum,
missing resource, cancellation, transport failure and unknown provider capacity
are ordinary failures and stop the operation. No extra reservation, cache,
timeout or global budget registry is added.

## Historical compatibility and rollout

Initial-history publication still enforces its existing encoded-record limits.
Adding source descriptors does not raise those limits or convert earlier
byte-bearing `ImagePart` histories.

Unmarked tools retain their generated contract and fingerprint: false
`NativeImage` is omitted from consumer-contract JSON. Marking a source changes
the fingerprint intentionally. Registry transports propagate the marker as
generated metadata; an old registry that loses it cannot represent this contract.

Before writing the first `image_source` message, upgrade every process that
decodes, validates, copies, exports or replays canonical messages: workflow
workers, runtime storage readers, history/summarization callers and application
copy/read models. Upgrade registry participants that serve the marked consumer
contract and install the host's generated kind manifest and reader. Raw model
servers that receive only resolved `ImagePart` bytes require no new source
decoder. Older canonical-message readers reject the new part rather than
silently dropping it, so rollback to such readers is unsafe while these records
remain reachable.

Keep each historical kind's generated schema, codec and reader available while
any original or copied conversation can reference it. That requirement is
independent of current tools, `Use`, tags, profiles and registry search results.
A host can retain the source-only generated manifest with the original producer
identifier after removing the executable tool. A changed descriptor contract
needs a distinct kind; conflicting shapes cannot replace a retained decoder.
Deleting a decoder is safe only after the owner proves no reachable record or
copy needs it.

Copies retain self-contained descriptors or rewrite them through the owner's
typed copy contract when resource identifiers change. They never require the
original runtime's tool-result store. The owner must preserve exact bytes and
define the new holder's authorization. Framework synthetic-copy tests do not
prove an application's copy, deletion, retention or transport behavior.
