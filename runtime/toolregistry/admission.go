// Package toolregistry defines the canonical wire protocol and stream naming
// helpers used by the tool registry gateway and tool providers/consumers.
package toolregistry

import (
	"fmt"
	"regexp"
	"time"
)

const (
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
)

var (
	admissionRevisionRegexp = regexp.MustCompile(AdmissionRevisionPattern)
	registrationTokenRegexp = regexp.MustCompile(RegistrationTokenPattern)
)

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
