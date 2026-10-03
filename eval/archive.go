// Package eval stores capture evidence independently of assessment. Hashes cover
// exact observation bytes and capture metadata. Reading an archive verifies its
// identity before any generated decoder or assessor consumes its contents.
package eval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

type (
	// Observation is the immutable result of one product capture attempt.
	Observation struct {
		// ID is the SHA-256 identity of all other fields in this record.
		ID string `json:"id"`
		// ScenarioID identifies the hook that produced this observation.
		ScenarioID string `json:"scenario_id"`
		// SchemaID identifies the exact generated observation schema.
		SchemaID string `json:"schema_id"`
		// Input retains the exact validated capture input bytes.
		Input []byte `json:"input,omitempty"`
		// Data retains exact encoded observation bytes. JSON archives use base64
		// so formatting cannot silently change the bytes or their identity.
		Data []byte `json:"data,omitempty"`
		// CapturedAt is zero when cancellation prevented capture from starting.
		CapturedAt time.Time `json:"captured_at"`
		// Duration measures product capture and observation encoding only.
		Duration time.Duration `json:"duration"`
		// Error records a failed capture, rather than fabricating product evidence.
		Error string `json:"error,omitempty"`
	}

	// Archive contains captured evidence that can support independent assessments.
	Archive struct {
		// ID is the SHA-256 identity of all other archive fields.
		ID string `json:"id"`
		// SuiteID identifies the suite whose hooks were executed.
		SuiteID string `json:"suite_id"`
		// StartedAt records when capture began.
		StartedAt time.Time `json:"started_at"`
		// Duration measures capture wall time, independently of assessment.
		Duration time.Duration `json:"duration"`
		// Provenance records caller-supplied product revision and fixture identities.
		// It must not contain credentials.
		Provenance map[string]string `json:"provenance,omitempty"`
		// Observations retains all selected scenarios, including capture errors.
		Observations []Observation `json:"observations"`
	}
)

// ReadArchive decodes one archive and rejects unknown fields, trailing data,
// altered records, and incomplete identities. It never executes product hooks.
func ReadArchive(reader io.Reader) (Archive, error) {
	var archive Archive
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&archive); err != nil {
		return Archive{}, fmt.Errorf("decode capture archive: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Archive{}, errors.New("capture archive contains trailing data")
	}
	if err := archive.Validate(); err != nil {
		return Archive{}, err
	}
	return archive, nil
}

// WriteTo verifies an archive and writes one JSON document without changing any
// evidence bytes. Callers choose and own the file or object-store destination.
func (a Archive) WriteTo(writer io.Writer) (int64, error) {
	if err := a.Validate(); err != nil {
		return 0, err
	}
	data, err := json.Marshal(a)
	if err != nil {
		return 0, fmt.Errorf("encode capture archive: %w", err)
	}
	return bytes.NewReader(append(data, '\n')).WriteTo(writer)
}

// Validate verifies content identity and the complete capture-record shape.
func (a Archive) Validate() error {
	if a.SuiteID == "" || len(a.Observations) == 0 || a.StartedAt.IsZero() || a.Duration < 0 {
		return errors.New("capture archive requires a suite, start time, and observations")
	}
	seen := make(map[string]bool, len(a.Observations))
	for _, observation := range a.Observations {
		if observation.ScenarioID == "" || observation.SchemaID == "" || seen[observation.ScenarioID] {
			return errors.New("capture archive contains a missing or duplicate scenario identity")
		}
		seen[observation.ScenarioID] = true
		if observation.Duration < 0 || (observation.Error == "" && (len(observation.Data) == 0 || observation.CapturedAt.IsZero())) {
			return fmt.Errorf("incomplete observation for %q", observation.ScenarioID)
		}
		id := observation.ID
		observation.ID = ""
		if id != digest(observation) {
			return fmt.Errorf("observation %q content hash does not match", observation.ScenarioID)
		}
	}
	id := a.ID
	a.ID = ""
	if id != digest(a) {
		return errors.New("capture archive content hash does not match")
	}
	return nil
}

// digest hashes constructed, JSON-serializable framework records. All floating
// values are validated at their public boundary before reaching this function.
func digest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("invalid constructed evaluation record: %v", err))
	}
	return bytesDigest(encoded)
}

func bytesDigest(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
