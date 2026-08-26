// Package mongo registers MongoDB-backed run event log storage for goa-ai agents.
//
// Use clients/mongo to migrate and build the low-level client, then pass it to
// NewStore to obtain a runlog.Store that persists append-only run events in
// stable sequence order.
package mongo
