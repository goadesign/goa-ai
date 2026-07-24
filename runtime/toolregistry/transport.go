// Package toolregistry owns registry transport identity and result retention.
//
// The gateway derives transport IDs from validated run/call metadata and
// selects one bounded sliding TTL carried through every result-stream user.
package toolregistry

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"
)

const (
	// MaxToolUseIDLength is the transport boundary shared by calls and results.
	MaxToolUseIDLength = 256
	// MaxToolCallWait bounds how long an executor waits for a provider result.
	MaxToolCallWait = 10 * time.Minute
	// ResultStreamTransportBudget preserves the stream beyond the maximum wait
	// for publication, acknowledgement, and transport scheduling.
	ResultStreamTransportBudget = time.Minute
	// MinResultStreamTTL is the shortest registry-selected sliding retention.
	MinResultStreamTTL = MaxToolCallWait + ResultStreamTransportBudget
	// DefaultResultStreamTTL is used when registry configuration omits retention.
	DefaultResultStreamTTL = 15 * time.Minute
	// MaxResultStreamTTL bounds orphan retention after a caller disappears.
	MaxResultStreamTTL = 24 * time.Hour

	// #nosec G101 -- this public identity domain is not a credential.
	toolUseIDDomain = "goa-ai/tool-registry-use/v1\x00"
)

// DeriveToolUseID returns the deterministic global transport identity for one
// model/provider call inside one run. Length delimiters prevent concatenation
// ambiguity while the domain separator prevents cross-protocol reuse.
func DeriveToolUseID(runID, toolCallID string) string {
	body := []byte(toolUseIDDomain)
	body = appendLengthDelimited(body, runID)
	body = appendLengthDelimited(body, toolCallID)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// ValidateToolUseID enforces the shared call/result transport boundary.
func ValidateToolUseID(toolUseID string) error {
	if len(toolUseID) == 0 || len(toolUseID) > MaxToolUseIDLength {
		return fmt.Errorf("tool use ID length must be between 1 and %d", MaxToolUseIDLength)
	}
	return nil
}

// ValidateResultStreamTTLMillis enforces the singular sliding retention
// contract carried in each call message and returned to its executor.
func ValidateResultStreamTTLMillis(ttlMillis int64) error {
	if ttlMillis < MinResultStreamTTL.Milliseconds() ||
		ttlMillis > MaxResultStreamTTL.Milliseconds() {
		return fmt.Errorf(
			"result stream TTL must be between %s and %s",
			MinResultStreamTTL,
			MaxResultStreamTTL,
		)
	}
	return nil
}

// appendLengthDelimited appends one string with an unambiguous uint64 byte count.
func appendLengthDelimited(body []byte, value string) []byte {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	body = append(body, length[:]...)
	return append(body, value...)
}
