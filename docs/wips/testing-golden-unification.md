Title: Unify Codegen Tests with Golden Files + Scoped Helpers

Desired Outcome

- A consistent, elegant testing strategy for code generation that:
  - Uses the shared golden utilities to compare generated files deterministically.
  - Avoids brittle string assertions and formatting-sensitive checks.
  - Preserves correctness with Goa’s NameScope and locators (e.g., Meta("struct:pkg:path")).
  - Keeps runtime tests focused on runtime behavior, and codegen tests focused on generated artifacts.

Plan (Reader Assumes No Prior Context)

1) Add a WIP doc (this file) that defines the target state, plan, and tracker.
2) Standardize on the existing testutil golden helpers for codegen tests:
   - Prefer golden file comparisons (AssertGo/Assert) over ad‑hoc Contains checks.
   - Normalize Go headers and line endings via the util.
3) Convert tests that look for textual snippets into golden‑based assertions or structured checks.
4) Add a simple internal transforms test that verifies adapter helpers are emitted for method‑backed tools.
5) Ensure tests that previously depended on service_toolset.go are either migrated to internal transforms or removed.
6) For quickstart/long text tests, prefer golden files with normalization over hand‑crafted substrings.
7) Avoid brittle whitespace assertions: rely on formatting or normalize before compare.
8) Document the test authoring workflow in codegen/agent/tests/README.md (how to add scenarios + goldens).
9) Keep runtime tests focused on runtime: assert typed reasons or a small set of stable messages.
10) Re‑run and fix any stragglers; make the suite pass cleanly.

Progress Tracker

- [x] 1) Add WIP doc describing target and plan
- [x] 2) Use shared golden helpers consistently in new/updated tests
- [x] 3) Replace fragile string checks where practical in this pass
- [x] 4) Add internal transforms emission test (smoke)
- [x] 5) Remove/migrate service_toolset expectations
- [x] 6) Convert quickstart test to golden (normalized markers)
- [x] 7) Eliminate whitespace brittleness in touched tests
- [x] 8) Update tests/README with the authoring workflow
- [x] 9) Keep runtime tests focused; adjust error messages to assert stable reasons
- [x] 10) Ensure go test ./... passes cleanly
