// This file verifies that a remote transport can restore generated correction
// guidance without turning provider failures or other terminal errors into
// recoverable model output.
package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/internal/correction"
)

type restorationTestCause struct {
	cause error
}

// Error describes the synthetic decoded validation failure used to verify
// errors.As without exposing provider output.
func (e *restorationTestCause) Error() string {
	return "decoded output failed its contract"
}

// Unwrap preserves the synthetic sentinel used to verify errors.Is.
func (e *restorationTestCause) Unwrap() error {
	return e.cause
}

func TestRestoreOutputValidationErrorPreservesBoundedEvidence(t *testing.T) {
	contract, err := NewRequestContract(&Request{})
	require.NoError(t, err)
	source := contract.RejectResponse(canonicalTextResponse(), errors.New("remote adapter rejected output"))

	restored, err := RestoreOutputValidationError(errors.Unwrap(source), source.Evidence(), source.Usage())

	require.NoError(t, err)
	require.Equal(t, source.Evidence(), restored.Evidence())
	require.Equal(t, source.Usage(), restored.Usage())
	require.ErrorContains(t, restored, "remote adapter rejected output")
	require.Empty(t, restored.RecoveryCorrection())
	rejected, err := restored.RejectedResponse()
	require.NoError(t, err)
	require.Nil(t, rejected)

	_, err = RestoreOutputValidationError(errors.New("invalid evidence"), ResponseEvidence{
		Present: true,
		Version: "unsupported",
		SHA256:  source.Evidence().SHA256,
		Size:    source.Evidence().Size,
	}, nil)
	require.ErrorContains(t, err, `unsupported version "unsupported"`)

	_, err = RestoreOutputValidationError(errors.New("invalid evidence"), ResponseEvidence{
		Present: true,
		Version: source.Evidence().Version,
		SHA256:  strings.Repeat("A", 64),
		Size:    1,
	}, nil)
	require.ErrorContains(t, err, "must use lowercase hexadecimal characters")
}

func TestRestoreOutputValidationErrorPreservesCauseIdentity(t *testing.T) {
	sentinel := errors.New("decoded rejection sentinel")
	cause := &restorationTestCause{cause: sentinel}

	terminal, err := RestoreOutputValidationError(cause, ResponseEvidence{Present: true}, nil)
	require.NoError(t, err)
	correctable, err := RestoreCorrectableOutputValidationError(
		terminal,
		`Field "query" must contain a JSON string.`,
	)
	require.NoError(t, err)

	for _, restored := range []*OutputValidationError{terminal, correctable} {
		require.ErrorIs(t, restored, sentinel)
		var target *restorationTestCause
		require.ErrorAs(t, restored, &target)
		require.Same(t, cause, target)
	}
}

func TestRestoreOutputValidationErrorRejectsContradictoryCauses(t *testing.T) {
	var typedNil *restorationTestCause
	nested := newOutputValidationError(
		errors.New("locally classified output rejection"),
		ResponseEvidence{Present: true},
		nil,
		nil,
	)
	provider := NewProviderError(
		"test",
		"complete",
		0,
		ProviderErrorKindUnavailable,
		"",
		"provider unavailable",
		"",
		true,
		nil,
	)
	tests := []struct {
		name    string
		cause   error
		wantErr string
	}{
		{
			name:    "nil",
			wantErr: "requires a cause",
		},
		{
			name:    "typed nil",
			cause:   typedNil,
			wantErr: "must not be typed nil",
		},
		{
			name:    "provider error",
			cause:   provider,
			wantErr: "must not contain ProviderError",
		},
		{
			name:    "cancellation",
			cause:   fmt.Errorf("remote call stopped: %w", context.Canceled),
			wantErr: "must not contain context cancellation",
		},
		{
			name:    "deadline",
			cause:   fmt.Errorf("remote call stopped: %w", context.DeadlineExceeded),
			wantErr: "must not contain context deadline",
		},
		{
			name:    "nested output validation error",
			cause:   fmt.Errorf("nested classification: %w", nested),
			wantErr: "must not contain OutputValidationError",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			restored, err := RestoreOutputValidationError(
				test.cause,
				ResponseEvidence{Present: true},
				nil,
			)
			require.Nil(t, restored)
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}

func TestRestoreOutputValidationErrorRejectsTerminalSentinels(t *testing.T) {
	sentinels := []struct {
		name string
		err  error
	}{
		{name: "streaming unsupported", err: ErrStreamingUnsupported},
		{name: "structured output unsupported", err: ErrStructuredOutputUnsupported},
		{name: "rate limited", err: ErrRateLimited},
		{name: "empty stream", err: ErrEmptyStream},
		{name: "token counting unsupported", err: ErrTokenCountingUnsupported},
	}
	forms := []struct {
		name  string
		cause func(error) error
	}{
		{
			name: "direct",
			cause: func(err error) error {
				return err
			},
		},
		{
			name: "wrapped",
			cause: func(err error) error {
				return fmt.Errorf("remote model failure: %w", err)
			},
		},
	}

	for _, sentinel := range sentinels {
		for _, form := range forms {
			t.Run(sentinel.name+"/"+form.name, func(t *testing.T) {
				terminal, err := RestoreOutputValidationError(
					form.cause(sentinel.err),
					ResponseEvidence{Present: true},
					nil,
				)
				require.Nil(t, terminal)
				require.ErrorContains(t, err, "must not contain")

				correctable, correctionErr := RestoreCorrectableOutputValidationError(
					terminal,
					`Field "query" must contain a JSON string.`,
				)
				require.Nil(t, correctable)
				require.ErrorContains(t, correctionErr, "requires a restored terminal error")
			})
		}
	}

	t.Run("ordinary decoded validation cause", func(t *testing.T) {
		terminal, err := RestoreOutputValidationError(
			errors.New("decoded field validation failed"),
			ResponseEvidence{Present: true},
			nil,
		)
		require.NoError(t, err)
		correctable, err := RestoreCorrectableOutputValidationError(
			terminal,
			`Field "query" must contain a JSON string.`,
		)
		require.NoError(t, err)
		require.Equal(t, `Field "query" must contain a JSON string.`, correctable.RecoveryCorrection())
	})

	t.Run("generated field validation cause", func(t *testing.T) {
		correction := `Field "query" must contain a JSON string.`
		terminal, err := RestoreOutputValidationError(
			&toolCallValidationError{
				toolName:   "catalog.lookup",
				correction: correction,
			},
			ResponseEvidence{Present: true},
			nil,
		)
		require.NoError(t, err)
		require.Empty(t, terminal.RecoveryCorrection())
		correctable, err := RestoreCorrectableOutputValidationError(terminal, correction)
		require.NoError(t, err)
		require.Equal(t, correction, correctable.RecoveryCorrection())
	})
}

func TestRestoreCorrectableOutputValidationErrorPreservesSafeFields(t *testing.T) {
	contract, err := NewRequestContract(&Request{})
	require.NoError(t, err)
	source := contract.RejectResponse(canonicalTextResponse(), errors.New("decoded rejection contained a submitted value"))
	correction := "\nField \"query\" must contain a JSON string.\n"

	terminal, err := RestoreOutputValidationError(
		errors.Unwrap(source),
		source.Evidence(),
		source.Usage(),
	)
	require.NoError(t, err)
	restored, err := RestoreCorrectableOutputValidationError(
		terminal,
		correction,
	)

	require.NoError(t, err)
	require.Empty(t, terminal.RecoveryCorrection())
	require.Equal(t, source.Evidence(), restored.Evidence())
	require.Equal(t, source.Usage(), restored.Usage())
	require.Equal(t, correction, restored.RecoveryCorrection())
	require.NotContains(t, restored.RecoveryCorrection(), "submitted value")
	rejected, err := restored.RejectedResponse()
	require.NoError(t, err)
	require.Nil(t, rejected)
}

func TestRestoreCorrectableOutputValidationErrorRequiresRestoredTerminal(t *testing.T) {
	correction := `Field "query" must contain a JSON string.`
	restored, err := RestoreCorrectableOutputValidationError(nil, correction)
	require.Nil(t, restored)
	require.ErrorContains(t, err, "requires a restored terminal error")

	local := newOutputValidationError(
		errors.New("locally classified output rejection"),
		ResponseEvidence{Present: true},
		nil,
		nil,
	)
	restored, err = RestoreCorrectableOutputValidationError(local, correction)
	require.Nil(t, restored)
	require.ErrorContains(t, err, "requires an error returned by RestoreOutputValidationError")

	terminal, err := RestoreOutputValidationError(
		errors.New("decoded validation failure"),
		ResponseEvidence{Present: true},
		nil,
	)
	require.NoError(t, err)
	restored, err = RestoreCorrectableOutputValidationError(terminal, correction)
	require.NoError(t, err)
	nested, err := RestoreCorrectableOutputValidationError(restored, correction)
	require.Nil(t, nested)
	require.ErrorContains(t, err, "requires a terminal restored error")
}

func TestRestoreCorrectableOutputValidationErrorValidatesCorrection(t *testing.T) {
	tests := []struct {
		name       string
		correction string
		wantErr    string
	}{
		{
			name:    "empty",
			wantErr: "requires correction guidance",
		},
		{
			name:       "blank",
			correction: " \t\n",
			wantErr:    "must not be blank",
		},
		{
			name:       "invalid UTF-8",
			correction: string([]byte{0xff}),
			wantErr:    "must be valid UTF-8",
		},
		{
			name:       "over byte limit",
			correction: strings.Repeat("x", correction.MaxBytes+1),
			wantErr:    "exceeds 4096 bytes",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			terminal, err := RestoreOutputValidationError(
				errors.New("decoded validation failure"),
				ResponseEvidence{Present: true},
				nil,
			)
			require.NoError(t, err)
			_, err = RestoreCorrectableOutputValidationError(
				terminal,
				test.correction,
			)
			require.ErrorContains(t, err, test.wantErr)
		})
	}

	accepted := strings.Repeat("x", correction.MaxBytes)
	terminal, err := RestoreOutputValidationError(
		errors.New("decoded validation failure"),
		ResponseEvidence{Present: true},
		nil,
	)
	require.NoError(t, err)
	restored, err := RestoreCorrectableOutputValidationError(
		terminal,
		accepted,
	)
	require.NoError(t, err)
	require.Equal(t, accepted, restored.RecoveryCorrection())
}
