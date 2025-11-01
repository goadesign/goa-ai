// Package codegen implements the Goa plugin responsible for turning the agents DSL
// expressions into runtime-ready Go packages. The plugin mirrors Goa's own
// codegen pipeline: expressions are collected during evaluation, converted into
// intermediary data structures during the prepare phase, then rendered through
// templates via goa.design/goa/v3/codegen.Files.
package codegen
