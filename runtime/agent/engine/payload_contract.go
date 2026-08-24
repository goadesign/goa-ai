package engine

// MaxPayloadBytes is the largest aggregate encoded value that may cross one
// workflow-engine call. Engine adapters must reject larger inputs and outputs
// before transport; runtime code must reduce large domain results to durable
// references before reaching this boundary.
const MaxPayloadBytes = 1 << 20
