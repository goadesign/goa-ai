// Package toolregistry owns registry transport identity and result retention.
//
// The gateway derives transport IDs from validated run/call metadata and
// selects one absolute expiration carried through every result-stream user.
package toolregistry

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
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
	// MinResultStreamTTL is the shortest retention from which the registry may
	// derive an absolute call expiration.
	MinResultStreamTTL = MaxToolCallWait + ResultStreamTransportBudget
	// DefaultResultStreamTTL is used when registry configuration omits retention.
	DefaultResultStreamTTL = 15 * time.Minute
	// MaxResultStreamTTL bounds orphan retention after a caller disappears.
	MaxResultStreamTTL = 24 * time.Hour
	// ToolUseIDPattern rejects the composite-key separator at non-Goa
	// call/result boundaries.
	ToolUseIDPattern = `^[^\x00]{1,256}$`

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
	if len(toolUseID) == 0 ||
		len(toolUseID) > MaxToolUseIDLength ||
		strings.ContainsRune(toolUseID, '\x00') {
		return fmt.Errorf("tool use ID must match %s", ToolUseIDPattern)
	}
	return nil
}

// ValidateExecutionDeadline enforces the structural absolute execution
// contract without using the caller's local clock to decide liveness.
func ValidateExecutionDeadline(deadline time.Time) error {
	if deadline.UnixMilli() <= 0 {
		return fmt.Errorf("execution deadline must be a positive Unix millisecond timestamp")
	}
	if !deadline.Equal(time.UnixMilli(deadline.UnixMilli())) {
		return fmt.Errorf("execution deadline must have millisecond precision")
	}
	return nil
}

// ValidateResultStreamExpiration enforces the structural absolute retention
// contract without using the caller's local clock to decide liveness.
func ValidateResultStreamExpiration(expiresAt time.Time) error {
	if expiresAt.UnixMilli() <= 0 {
		return fmt.Errorf("result stream expiration must be a positive Unix millisecond timestamp")
	}
	if !expiresAt.Equal(time.UnixMilli(expiresAt.UnixMilli())) {
		return fmt.Errorf("result stream expiration must have millisecond precision")
	}
	return nil
}

// ValidateToolCallRef enforces the registry result reference's structural
// identity and expiration contract. It deliberately does not decide liveness.
func ValidateToolCallRef(ref ToolCallRef) error {
	if err := ValidateToolUseID(ref.ToolUseID); err != nil {
		return err
	}
	if err := ValidateRegistrationToken(ref.RegistrationToken); err != nil {
		return err
	}
	if err := ValidateExecutionDeadline(ref.ExecutionDeadline); err != nil {
		return err
	}
	if err := ValidateResultStreamExpiration(ref.ResultStreamExpiresAt); err != nil {
		return err
	}
	if !ref.ExecutionDeadline.Before(ref.ResultStreamExpiresAt) {
		return fmt.Errorf("execution deadline must precede result stream expiration")
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
