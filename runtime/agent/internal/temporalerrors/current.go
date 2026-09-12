package temporalerrors

// Current Temporal failures preserve valid text without individual field limits.
// The application type selects one concrete saved format before details are
// decoded. Historical types still use their original readers and wrapping rules.

import (
	"fmt"
	"unicode/utf8"

	"go.temporal.io/sdk/temporal"

	"goa.design/goa-ai/runtime/agent/internal/errorevidence"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
)

type (
	// currentProviderDetails retains the provider's typed facts independently
	// of the outer error's rendered context. It never serializes an SDK object.
	currentProviderDetails struct {
		Provider   string
		Operation  string
		HTTPStatus int
		Kind       string
		Code       string
		Message    string
		RequestID  string
		Retryable  bool
	}

	// currentGenericDetails retains a custom application's owned message and
	// type, without serializing its arbitrary details or native cause objects.
	currentGenericDetails struct {
		OriginalType string
		Message      string
		Retryable    bool
	}
)

const (
	currentProviderApplicationType   = "goa_ai.provider_error.v3"
	currentGenericApplicationType    = "goa_ai.generic_error.v3"
	currentOutputApplicationType     = "goa_ai.output_contract_error.v3"
	currentInvalidApplicationType    = "goa_ai.invalid_reserved_error.v3"
	requestValidationApplicationType = "goa_ai.request_validation_error"
)

// Wrap preserves failure classification and complete valid diagnostic text for
// Temporal. Cancellation stays canceled, and provider/custom retry settings
// stay unchanged. Existing saved formats retain their historical conversion.
func Wrap(err error) error {
	if err == nil {
		return nil
	}
	if isNilErrorValue(err) {
		return wrapCurrentInvalid(fmt.Errorf("error graph contains a typed nil node"))
	}
	if CancellationOnly(err) {
		return temporal.NewCanceledError("activity canceled")
	}
	//nolint:errorlint // Only a direct custom application error owns this retry setting.
	if app, ok := err.(*temporal.ApplicationError); ok && !reservedApplicationType(app.Type()) {
		return wrapCurrentGeneric(app, app.Type(), !app.NonRetryable())
	}
	// Historical direct invalid errors were idempotent even before validation.
	//nolint:errorlint // Only the exact saved error takes the historical path.
	if app, ok := err.(*temporal.ApplicationError); ok && historicalApplicationType(app.Type()) {
		return wrapHistorical(err)
	}
	classified := classify(err)
	if classified.application != nil && historicalApplicationType(classified.application.Type()) {
		return wrapHistorical(err)
	}
	//nolint:errorlint // A validated saved error must not be rewritten on replay.
	if classified.application != nil && classified.application == err {
		return err
	}
	switch classified.kind {
	case errorKindRequestValidation:
		return temporal.NewNonRetryableApplicationError(
			errorevidence.DiagnosticMessage(errorevidence.Text(err)), requestValidationApplicationType, nil,
		)
	case errorKindOutputContract:
		message := errorevidence.Text(err)
		if classified.output != nil {
			cause := classified.output.Unwrap()
			// Only an immediate model validation error owns the separate cause
			// selected for diagnostics, rather than its model-facing summary.
			//nolint:errorlint // Do not search a joined or wrapped graph for another owner.
			if validation, ok := cause.(*model.OutputValidationError); ok && validation != nil {
				cause = validation.Unwrap()
			}
			message = errorevidence.Text(cause)
		}
		return temporal.NewNonRetryableApplicationError(
			errorevidence.DiagnosticMessage(message), currentOutputApplicationType, nil,
			outputContractErrorDetails{Origin: string(classified.origin)},
		)
	case errorKindProvider:
		provider := classified.provider
		details := currentProviderDetails{
			Provider:   errorevidence.DiagnosticMessage(provider.Provider()),
			Operation:  errorevidence.DiagnosticMessage(provider.Operation()),
			HTTPStatus: provider.HTTPStatus(),
			Kind:       string(provider.Kind()),
			Code:       errorevidence.DiagnosticMessage(provider.Code()),
			Message:    errorevidence.DiagnosticMessage(provider.Message()),
			RequestID:  errorevidence.DiagnosticMessage(provider.RequestID()),
			Retryable:  provider.Retryable(),
		}
		if validationErr := validateCurrentProviderDetails(details); validationErr != nil {
			return wrapCurrentInvalid(validationErr)
		}
		return temporal.NewApplicationErrorWithOptions(errorevidence.DiagnosticMessage(errorevidence.Text(err)),
			currentProviderApplicationType, temporal.ApplicationErrorOptions{
				NonRetryable: !provider.Retryable(),
				Details:      []any{details},
			})
	case errorKindGeneric:
		return temporal.NewApplicationErrorWithOptions(errorevidence.DiagnosticMessage(errorevidence.Text(err)),
			currentGenericApplicationType, temporal.ApplicationErrorOptions{
				NonRetryable: classified.application.NonRetryable(),
				Details:      []any{classified.currentGeneric},
			})
	case errorKindInvalidReserved:
		return wrapCurrentInvalid(classified.invalid)
	case errorKindNone:
		if IsNativeTimeout(err) {
			return err
		}
		return wrapCurrentGeneric(err, "", true)
	default:
		panic(fmt.Sprintf("temporalerrors: unknown classification kind %d", classified.kind))
	}
}

// wrapCurrentGeneric keeps owned text separate from outer rendered context.
func wrapCurrentGeneric(err error, originalType string, retryable bool) error {
	diagnostic := errorevidence.Text(err)
	message := diagnostic
	//nolint:errorlint // Only a direct custom application error owns this detail field.
	if app, ok := err.(*temporal.ApplicationError); ok {
		message = app.Message()
	}
	return temporal.NewApplicationErrorWithOptions(errorevidence.DiagnosticMessage(diagnostic),
		currentGenericApplicationType, temporal.ApplicationErrorOptions{
			NonRetryable: !retryable,
			Details: []any{currentGenericDetails{
				OriginalType: errorevidence.DiagnosticMessage(originalType),
				Message:      errorevidence.DiagnosticMessage(message),
				Retryable:    retryable,
			}},
		})
}

// wrapCurrentInvalid reports malformed saved errors without retrying them.
func wrapCurrentInvalid(err error) error {
	return temporal.NewNonRetryableApplicationError(
		errorevidence.DiagnosticMessage(errorevidence.Text(err)), currentInvalidApplicationType, nil,
	)
}

// validateCurrentApplication checks the shared cause-free UTF-8 contract. The
// selected concrete decoder below checks classification and typed facts.
func validateCurrentApplication(app *temporal.ApplicationError) error {
	if app.Unwrap() != nil {
		return fmt.Errorf("reserved error has a cause")
	}
	if !utf8.ValidString(app.Message()) {
		return fmt.Errorf("reserved diagnostic message is not valid UTF-8")
	}
	return nil
}

// classifyRequestValidation accepts a saved local request rejection only when
// it remains terminal and carries its diagnostic in the message alone.
func classifyRequestValidation(app *temporal.ApplicationError) classification {
	if err := validateCurrentApplication(app); err != nil {
		return invalidReserved("request validation error: %v", err)
	}
	if !app.NonRetryable() || app.HasDetails() {
		return invalidReserved("request validation error must be nonretryable without details")
	}
	return classification{kind: errorKindRequestValidation, application: app}
}

// classifyCurrentOutput reads a saved output failure without changing origin.
func classifyCurrentOutput(app *temporal.ApplicationError) classification {
	if err := validateCurrentApplication(app); err != nil {
		return invalidReserved("output error: %v", err)
	}
	if !app.NonRetryable() {
		return invalidReserved("reserved output contract error is retryable")
	}
	var details outputContractErrorDetails
	if err := decodeApplicationDetails(app, &details); err != nil {
		return invalidReserved("output details: %v", err)
	}
	origin := planner.OutputContractOrigin(details.Origin)
	if !validOutputOrigin(origin) {
		return invalidReserved("reserved output contract error has invalid origin %q", details.Origin)
	}
	return classification{kind: errorKindOutputContract, origin: origin, application: app}
}

// classifyCurrentProvider restores exact typed provider facts from saved text.
func classifyCurrentProvider(app *temporal.ApplicationError) classification {
	if err := validateCurrentApplication(app); err != nil {
		return invalidReserved("provider error: %v", err)
	}
	var details currentProviderDetails
	if err := decodeApplicationDetails(app, &details); err != nil {
		return invalidReserved("provider details: %v", err)
	}
	if err := validateCurrentProviderDetails(details); err != nil {
		return invalidReserved("provider details: %v", err)
	}
	if app.NonRetryable() == details.Retryable {
		return invalidReserved("provider retryability conflicts with its saved details")
	}
	return classification{
		kind: errorKindProvider,
		provider: model.NewProviderError(details.Provider, details.Operation, details.HTTPStatus,
			model.ProviderErrorKind(details.Kind), details.Code, details.Message, details.RequestID, details.Retryable,
			fmt.Errorf("%s", app.Message())),
		application: app,
	}
}

// validateCurrentProviderDetails enforces provider facts when native errors
// enter Temporal and when saved details return from it. Text has no field cap.
func validateCurrentProviderDetails(details currentProviderDetails) error {
	for _, text := range []string{details.Provider, details.Operation, details.Kind, details.Code, details.Message, details.RequestID} {
		if !utf8.ValidString(text) {
			return fmt.Errorf("provider details contain invalid UTF-8")
		}
	}
	if details.Provider == "" {
		return fmt.Errorf("provider is empty")
	}
	if details.HTTPStatus != 0 && (details.HTTPStatus < 100 || details.HTTPStatus > 599) {
		return fmt.Errorf("HTTP status %d is outside 100-599", details.HTTPStatus)
	}
	if !validProviderKind(model.ProviderErrorKind(details.Kind)) {
		return fmt.Errorf("invalid provider kind %q", details.Kind)
	}
	return nil
}

// classifyCurrentGeneric validates a saved ordinary error and its retry setting.
func classifyCurrentGeneric(app *temporal.ApplicationError) classification {
	if err := validateCurrentApplication(app); err != nil {
		return invalidReserved("generic error: %v", err)
	}
	var details currentGenericDetails
	if err := decodeApplicationDetails(app, &details); err != nil {
		return invalidReserved("generic details: %v", err)
	}
	if !utf8.ValidString(details.OriginalType) || !utf8.ValidString(details.Message) {
		return invalidReserved("generic details contain invalid UTF-8")
	}
	if app.NonRetryable() == details.Retryable {
		return invalidReserved("generic retryability conflicts with its saved details")
	}
	return classification{kind: errorKindGeneric, currentGeneric: details, application: app}
}

// classifyCurrentInvalid accepts only cause-free terminal diagnostic failures.
func classifyCurrentInvalid(app *temporal.ApplicationError) classification {
	if err := validateCurrentApplication(app); err != nil {
		return invalidReserved("invalid-envelope error: %v", err)
	}
	if !app.NonRetryable() || app.HasDetails() {
		return invalidReserved("invalid-envelope error must be nonretryable without details")
	}
	return classification{kind: errorKindInvalidReserved, invalid: fmt.Errorf("%s", app.Message()), application: app}
}
