// Package toolregistry defines the canonical wire protocol and stream naming
// helpers used by the tool registry gateway and tool providers/consumers.
package toolregistry

import (
	"fmt"
	"regexp"
	"time"

	internaladmission "goa.design/goa-ai/internal/toolregistry/admission"
)

const (
	// WireProtocolVersion is the only tool registry message protocol accepted by
	// this runtime. Provider registration and consumer CallTool/RetryTool requests
	// carry it explicitly so the registry rejects binaries that encode another
	// ToolCallMessage or ToolResultMessage contract before side effects.
	WireProtocolVersion = 9
	// AdmissionRevisionPattern is the canonical deployment-issued admission
	// revision syntax shared by the Goa boundary and provider lifecycle config.
	AdmissionRevisionPattern = `^[A-Za-z0-9][A-Za-z0-9._:/@+\-]{0,255}$`
	// RegistrationTokenPattern is the lowercase SHA-256 admission-generation
	// identity syntax shared by Goa and Pulse message boundaries.
	// #nosec G101 -- this public validation pattern is not a credential.
	RegistrationTokenPattern = `^[0-9a-f]{64}$`
	// MinProviderLeaseDuration exceeds the default provider shutdown margin,
	// registration attempt, and maximum retry-delay budget.
	MinProviderLeaseDuration = 45 * time.Second
	// MaxProviderLeaseDuration bounds registry-issued leases so overflow and
	// operationally stale provider membership cannot become valid state.
	MaxProviderLeaseDuration = 24 * time.Hour
	// ResultStreamMaxLen bounds retained best-effort delivery history. The
	// authoritative terminal message remains in the registry call record and
	// can be restored after this stream trims it.
	ResultStreamMaxLen = 512
	// MaxToolOutputDeltaCount bounds best-effort fragments retained for one
	// call before the registry suppresses further output.
	MaxToolOutputDeltaCount = 256
	// MaxToolOutputDeltaBytes bounds one UTF-8 output fragment at the registry
	// API boundary.
	MaxToolOutputDeltaBytes = 64 * 1024
)

var (
	admissionRevisionRegexp = regexp.MustCompile(AdmissionRevisionPattern)
	registrationTokenRegexp = regexp.MustCompile(RegistrationTokenPattern)
)

// ValidateWireProtocolVersion rejects provider and consumer binaries that do
// not implement the registry's one canonical message envelope.
func ValidateWireProtocolVersion(version int) error {
	if version != WireProtocolVersion {
		return fmt.Errorf(
			"tool registry wire protocol version must be %d, got %d",
			WireProtocolVersion,
			version,
		)
	}
	return nil
}

// ValidateAdmissionRevision rejects revisions that cannot be admitted through
// the registry contract. Callers use it for non-Goa boundaries such as provider
// process configuration and persisted catalog rehydration.
func ValidateAdmissionRevision(revision string) error {
	if !admissionRevisionRegexp.MatchString(revision) {
		return fmt.Errorf("admission revision must match %s", AdmissionRevisionPattern)
	}
	return nil
}

// ValidateRegistrationToken rejects values that are not canonical lowercase
// SHA-256 admission-generation identities.
func ValidateRegistrationToken(token string) error {
	if !registrationTokenRegexp.MatchString(token) {
		return fmt.Errorf("registration token must match %s", RegistrationTokenPattern)
	}
	return nil
}

// RegistrationToken derives the exact routing identity for one generated
// schema fingerprint and deployment admission revision using this runtime's
// fixed wire protocol version.
func RegistrationToken(schemaFingerprint, admissionRevision string) (string, error) {
	if err := ValidateAdmissionRevision(admissionRevision); err != nil {
		return "", err
	}
	return internaladmission.RegistrationToken(
		schemaFingerprint,
		admissionRevision,
		WireProtocolVersion,
	)
}
