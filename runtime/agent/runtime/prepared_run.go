// Package runtime stores compact references to exact accepted preparations.
// Complete workflow requests live only in the owning store's bounded records.
package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
	"goa.design/goa-ai/runtime/agent/storage"
)

type (
	// PreparedRun identifies one complete accepted preparation. Its serialized
	// value contains identity and publication positions, never history or policy.
	PreparedRun          struct{ reference preparedRunReference }
	preparedRunReference struct {
		Version      string                       `json:"version"`
		Operation    storage.PreparationOperation `json:"operation"`
		HistoryEndID string                       `json:"history_end_id"`
		EndID        string                       `json:"end_id"`
		Bytes        int64                        `json:"bytes"`
		Digest       string                       `json:"digest"`
	}
)

const preparedRunReferenceVersion = "goa-ai-prepared-run-v3"

// The owning store bounds a complete declaration, so its identity text is
// already bounded by MaxSeedCommandBytes. Add the full reference framing at
// maximum position/digest widths; no compiled request bytes belong here.
var preparedReferenceByteLimit = derivePreparedReferenceByteLimit()

func derivePreparedReferenceByteLimit() int {
	ref := preparedRunReference{Version: preparedRunReferenceVersion, HistoryEndID: "9223372036854775807", EndID: "9223372036854775807", Bytes: int64(startrecipe.PreparedRequestByteLimit()), Digest: strings.Repeat("f", 2*sha256.Size)}
	data, err := json.Marshal(ref)
	if err != nil {
		panic(err)
	}
	return storage.MaxSeedCommandBytes + len(data)
}

// ParsePreparedRun accepts only the current canonical reference format.
func ParsePreparedRun(data []byte) (*PreparedRun, error) {
	if len(data) > preparedReferenceByteLimit {
		return nil, preparedRunContractError(errors.New("decode prepared run: reference exceeds byte limit"))
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var ref preparedRunReference
	if err := decoder.Decode(&ref); err != nil {
		return nil, preparedRunContractError(fmt.Errorf("decode prepared run: %w", err))
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, preparedRunContractError(errors.New("decode prepared run: trailing data"))
	}
	if ref.Version != preparedRunReferenceVersion {
		return nil, preparedRunContractError(errors.New("decode prepared run: unsupported version"))
	}
	if err := storage.ValidatePreparationOperation(ref.Operation); err != nil {
		return nil, preparedRunContractError(err)
	}
	digest, err := hex.DecodeString(ref.Digest)
	if err != nil || len(digest) != sha256.Size || ref.HistoryEndID == "" || ref.EndID == "" || ref.Bytes <= 0 || ref.Bytes > int64(startrecipe.PreparedRequestByteLimit()) {
		return nil, preparedRunContractError(errors.New("decode prepared run: invalid publication reference"))
	}
	canonical, err := json.Marshal(ref)
	if err != nil {
		return nil, preparedRunContractError(err)
	}
	if !bytes.Equal(canonical, data) {
		return nil, preparedRunContractError(errors.New("decode prepared run: noncanonical reference"))
	}
	return &PreparedRun{reference: ref}, nil
}

// RunID returns the workflow identifier allocated to the accepted operation.
func (p *PreparedRun) RunID() string {
	if p == nil || p.reference.Operation.RunID == "" {
		panic("runtime: prepared run is required")
	}
	return p.reference.Operation.RunID
}

// MarshalBinary serializes only the exact accepted preparation reference.
func (p *PreparedRun) MarshalBinary() ([]byte, error) {
	if p == nil || p.reference.Operation.RunID == "" {
		return nil, preparedRunContractError(errors.New("prepared run is required"))
	}
	return json.Marshal(p.reference)
}

// StartPrepared submits the exact engine request held by prepared. It first
// checks the request against this client's generated agent definition.
func (c *agentClient) StartPrepared(ctx context.Context, prepared *PreparedRun) (engine.WorkflowHandle, error) {
	if prepared == nil || prepared.reference.Operation.AgentID == "" {
		return nil, preparedRunContractError(errors.New("prepared run is required"))
	}
	if prepared.reference.Operation.AgentID != string(c.definition.route.ID) {
		return nil, preparedRunContractError(fmt.Errorf(
			"prepared run belongs to agent %q, not %q",
			prepared.reference.Operation.AgentID,
			c.definition.route.ID,
		))
	}

	// Revalidate the stored workflow input and launch settings against the
	// current generated definition before submitting either value.
	accepted, found, err := c.r.Store.FindRunPreparation(ctx, prepared.reference.Operation)
	if err != nil {
		return nil, err
	}
	if !found || accepted.Seed.EndID != prepared.reference.HistoryEndID || accepted.EndID != prepared.reference.EndID || accepted.PreparedBytes != prepared.reference.Bytes {
		return nil, preparedRunContractError(storage.ErrSeedConflict)
	}
	data, err := readPreparation(ctx, c.r.Store, accepted)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != prepared.reference.Digest {
		return nil, preparedRunContractError(storage.ErrSeedConflict)
	}
	request, err := startrecipe.ParsePreparedRequest(data)
	if err != nil {
		return nil, preparedRunContractError(err)
	}
	if request.AgentID != prepared.reference.Operation.AgentID || request.Request.ID != prepared.reference.Operation.RunID || request.Request.Input.SessionID != prepared.reference.Operation.SessionID {
		return nil, preparedRunContractError(storage.ErrSeedConflict)
	}
	input := *request.Request.Input
	seed := accepted.Seed
	if input.SeedEndID != seed.EndID {
		return nil, preparedRunContractError(storage.ErrSeedConflict)
	}
	if seed.Declaration.AgentID != string(input.AgentID) || seed.Declaration.SessionID != input.SessionID {
		return nil, preparedRunContractError(storage.ErrRunRecordOwnerMismatch)
	}
	if input.Continuation != nil {
		checkpoint, err := prepareContinuation(&input, c.definition)
		if err != nil {
			return nil, preparedRunContractError(err)
		}
		if seed.Declaration.Kind != storage.SeedContinuation ||
			seed.Declaration.SourceRunID != checkpoint.PreviousRunID ||
			seed.Declaration.SourceEndID != checkpoint.HistoryEndID {
			return nil, preparedRunContractError(storage.ErrSeedConflict)
		}
	} else if seed.Declaration.Kind == storage.SeedContinuation {
		return nil, preparedRunContractError(storage.ErrSeedConflict)
	}
	launch := workflowLaunchSettings{
		taskQueue:        request.TaskQueueOverride,
		memo:             request.Request.Memo,
		searchAttributes: request.Request.SearchAttributes,
	}
	expected, err := prepareRunWithDefinition(&input, launch, c.definition, input.SessionID != "")
	if err != nil {
		return nil, preparedRunContractError(err)
	}
	expectedSnapshot, err := startrecipe.SnapshotRequest(expected)
	if err != nil {
		return nil, preparedRunContractError(err)
	}
	if request.Digest != expectedSnapshot.Digest {
		return nil, preparedRunContractError(errors.New("prepared engine request does not match its run input and agent definition"))
	}
	return c.r.startWorkflow(ctx, expectedSnapshot.Request)
}

// publishPreparedRun stores the complete compiled engine request once, then
// returns the compact value that applications retain for recovery and start.
func publishPreparedRun(ctx context.Context, writer *initialHistoryWriter, agentID agent.Ident, request engine.WorkflowStartRequest, taskQueueOverride, commandID string) (*PreparedRun, error) {
	if request.Input.SeedEndID == "" {
		return nil, preparedRunContractError(errors.New("published initial history is required"))
	}
	compiled, err := startrecipe.NewPreparedRequest(string(agentID), request, taskQueueOverride)
	if err != nil {
		return nil, err
	}
	data, err := compiled.MarshalBinary()
	if err != nil {
		return nil, preparedRunContractError(err)
	}
	historyEnd := writer.endID
	if err := writer.publish(ctx, data); err != nil {
		return nil, err
	}
	operation := storage.PreparationOperation{AgentID: string(agentID), RunID: request.ID, SessionID: request.Input.SessionID, CommandID: commandID}
	return preparedReference(operation, historyEnd, writer.endID, data), nil
}

func preparedReference(operation storage.PreparationOperation, historyEnd, end string, data []byte) *PreparedRun {
	digest := sha256.Sum256(data)
	return &PreparedRun{reference: preparedRunReference{Version: preparedRunReferenceVersion, Operation: operation, HistoryEndID: historyEnd, EndID: end, Bytes: int64(len(data)), Digest: hex.EncodeToString(digest[:])}}
}

// preparedRunContractError marks invalid retained references and engine values.
func preparedRunContractError(err error) error {
	return fmt.Errorf("%w: %w", ErrPreparedRunRejected, err)
}
