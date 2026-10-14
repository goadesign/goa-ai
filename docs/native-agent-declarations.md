# Complete native Agent tool declarations

The Registry validates each native Agent toolset as a complete collection before
registration, replacement, or loading saved declarations. It uses the same
collection compiler used by service declarations and runtime resolution.

Every tool name must be qualified and unique. A paging source and its continuation
must both be present when the contract names separate tools, and their paging
metadata must agree. Every member must target an Agent. A complete group may
contain different Agent targets, worker routes, configuration references,
qualified namespaces, and unrelated metadata. Bounds without paging and valid
self-continuing tools remain supported.

Previously, native registration compiled each tool separately. It could save an
unqualified tool or an orphan continuation that later failed resolution. Such
new registrations and replacements now return `validation_error` before any
catalog write. A valid existing registration still returns its exact saved token
and declaration on retry.

## Upgrade required

Inventory all saved native definitions before upgrading, including retired
entries. Active discovery alone cannot prove that the saved catalog is valid:
startup validates retained definitions too. Repair invalid groups through the
existing owner registration/replacement APIs while a matching owner can still
read them. Retiring an invalid group does not remove its saved definition.
If repairing a retired group reactivates it, restore its intended retirement
before allowing consumers to use it.

Recheck if old writers can still register declarations after the inventory.
Activate the new validator only after all saved native groups satisfy the
collection contract and incompatible writers have been removed. No schema
regeneration, read-only fallback, or separate relaxed reader is introduced by
this compiler correction. Rollback must preserve complete declarations and the
Registry's existing saved-protocol compatibility requirements.
