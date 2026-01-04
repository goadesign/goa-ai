// Package mongo registers MongoDB-backed run event log storage for goa-ai agents.
//
// Use clients/mongo to build the low-level client and pass it to NewStore to
// obtain a runlog.Store that persists append-only run events.
package mongo
