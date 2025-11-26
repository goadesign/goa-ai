// Package mongo provides a MongoDB-backed implementation of the agents runtime
// session store. Build the low-level client via features/session/mongo/clients/mongo
// and pass it to NewStore so higher-level services can persist run metadata outside
// the core runtime.
package mongo
