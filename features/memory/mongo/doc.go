// Package mongo registers MongoDB-backed memory storage for goa-ai agents. Use
// clients/mongo to build the low-level client and pass it to NewStore to obtain
// a runtime.MemoryStore that persists agent transcripts per (agent, run) pair.
package mongo
