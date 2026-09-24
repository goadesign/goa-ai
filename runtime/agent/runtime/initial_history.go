package runtime

// Preparation publishes initial history before producing an engine request.
// The same ordered writer serves caller processes, child activities, and direct
// workflow child calls. Callers never choose record positions or chunk sizes.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/transcript"
)

type (
	seedWriter interface {
		BeginRunSeed(context.Context, storage.SeedDeclaration) (storage.RunSeed, error)
		AppendRunSeed(context.Context, storage.SeedAppend) (string, error)
		PublishRunSeed(context.Context, storage.SeedPublication) error
	}

	// initialHistoryWriter carries only one run's append position. Each command
	// is frozen before submission, so activity retries cannot change its bytes.
	initialHistoryWriter struct {
		store     seedWriter
		runID     string
		attemptID string
		endID     string
		next      int
	}

	// workflowSeedWriter uses bounded storage activities when a caller invokes
	// ExecuteAgentChild from deterministic workflow code.
	workflowSeedWriter struct {
		r *Runtime
	}
)

// stageLiteralHistory validates and uploads history; it cannot execute until
// publish also binds the complete compiled result.
func stageLiteralHistory(ctx context.Context, store seedWriter, declaration storage.SeedDeclaration, messages []*model.Message) (*initialHistoryWriter, error) {
	if err := transcript.ValidatePlannerTranscript(messages); err != nil {
		return nil, fmt.Errorf("runtime: invalid initial transcript: %w", err)
	}
	if err := validateSeedPromptFacts(declaration.SessionID, declaration.RenderedPrompts); err != nil {
		return nil, err
	}
	if _, err := store.BeginRunSeed(ctx, declaration); err != nil {
		return nil, err
	}
	writer := &initialHistoryWriter{store: store, runID: declaration.RunID, attemptID: declaration.AttemptID, endID: storage.EmptySeedEndID}
	if err := writer.appendMessages(ctx, messages); err != nil {
		return nil, err
	}
	return writer, nil
}

// publish stores the compiled result in bounded parts after the history and
// atomically accepts both. A lost publication reply leaves the exact result
// available through the original operation.
func (w *initialHistoryWriter) publish(ctx context.Context, prepared []byte) error {
	seedEnd := w.endID
	total := int64(len(prepared))
	for len(prepared) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		command := w.command()
		// Binary parts use JSON base64 encoding. Start with the command maximum,
		// then reduce against the actual workflow converter and canonical codec.
		size := min(len(prepared), storage.MaxSeedCommandBytes*3/4)
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			command.Record.Prepared = prepared[:size]
			err := storage.ValidateSeedAppend(command)
			if err == nil {
				var encodedBytes int
				encodedBytes, err = seedCommandSize(command)
				if err == nil && encodedBytes > storage.MaxSeedCommandBytes {
					err = errors.New("compiled preparation part exceeds seed command limit")
				}
			}
			if err == nil {
				break
			}
			if size == 1 {
				return err
			}
			size /= 2
		}
		if err := w.append(ctx, command); err != nil {
			return err
		}
		prepared = prepared[size:]
	}
	return w.store.PublishRunSeed(ctx, storage.SeedPublication{RunID: w.runID, AttemptID: w.attemptID, SeedEndID: seedEnd, EndID: w.endID, PreparedBytes: total})
}

// appendMessages encodes each complete message once, then partitions those
// bytes. A message that cannot fit alone uses contiguous literal parts. The
// size includes the actual workflow storage-command envelope, so a
// larger logical import never becomes one oversized activity.
func (w *initialHistoryWriter) appendMessages(ctx context.Context, messages []*model.Message) error {
	var encodedNext []byte
	for len(messages) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		command := w.command()
		command.Record.Messages = rawjson.Message("[]")
		encodedBytes, err := seedCommandSize(command)
		if err != nil {
			return err
		}
		// JSON preserves raw literal bytes. The empty array's two bytes are
		// replaced by the bounded array assembled below.
		overhead := encodedBytes - 2
		available := storage.MaxSeedCommandBytes - overhead
		if available <= 2 {
			return errors.New("initial-history command identity exceeds its byte limit")
		}
		data := make([]byte, 0, min(available, 4096))
		data = append(data, '[')
		count := 0
		for count < len(messages) {
			if err := ctx.Err(); err != nil {
				return err
			}
			if encodedNext == nil {
				encodedNext, err = json.Marshal(messages[count])
				if err != nil {
					return fmt.Errorf("encode initial message: %w", err)
				}
			}
			separator := 0
			if count > 0 {
				separator = 1
			}
			if len(data)+separator+len(encodedNext)+1 > available {
				if count == 0 {
					literal := make([]byte, 0, len(encodedNext)+2)
					literal = append(literal, '[')
					literal = append(literal, encodedNext...)
					literal = append(literal, ']')
					if err := w.appendLiteralParts(ctx, literal); err != nil {
						return err
					}
					messages, encodedNext = messages[1:], nil
				}
				break
			}
			if separator != 0 {
				data = append(data, ',')
			}
			data = append(data, encodedNext...)
			encodedNext = nil
			count++
		}
		if count == 0 {
			continue
		}
		data = append(data, ']')
		command.Record.Messages = data
		if err := w.append(ctx, command); err != nil {
			return err
		}
		messages = messages[count:]
	}
	return nil
}

// appendLiteralParts derives each part's capacity from its complete encoded
// command. The byte slice may split JSON tokens or UTF-8; only Final closes it.
func (w *initialHistoryWriter) appendLiteralParts(ctx context.Context, data []byte) error {
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		command := w.command()
		command.Record.LiteralPart = &storage.LiteralPart{Data: []byte{0}}
		encodedBytes, err := seedCommandSize(command)
		if err != nil {
			return err
		}
		overhead := encodedBytes - base64.StdEncoding.EncodedLen(1)
		capacity := (storage.MaxSeedCommandBytes - overhead) / 4 * 3
		if capacity < 1 {
			return errors.New("initial-history command identity exceeds its byte limit")
		}
		size := min(len(data), capacity)
		command.Record.LiteralPart = &storage.LiteralPart{Data: data[:size], Final: size == len(data)}
		if err := w.append(ctx, command); err != nil {
			return err
		}
		data = data[size:]
	}
	return nil
}

func (w *initialHistoryWriter) appendPrefix(ctx context.Context, prefix *storage.HistoryPrefix) error {
	command := w.command()
	command.Record.Prefix = prefix
	return w.append(ctx, command)
}

func (w *initialHistoryWriter) command() storage.SeedAppend {
	return storage.SeedAppend{
		RunID:     w.runID,
		AttemptID: w.attemptID,
		Record:    storage.SeedRecord{Key: "initial-" + strconv.Itoa(w.next), PreviousID: w.endID},
	}
}

func (w *initialHistoryWriter) append(ctx context.Context, command storage.SeedAppend) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := storage.ValidateSeedAppend(command); err != nil {
		return err
	}
	// Use the real converter on the complete command even when this caller
	// writes directly through the owning store API.
	size, err := seedCommandSize(command)
	if err != nil {
		return err
	}
	if size > storage.MaxSeedCommandBytes {
		return fmt.Errorf("initial-history command exceeds the %d-byte seed command limit", storage.MaxSeedCommandBytes)
	}
	end, err := w.store.AppendRunSeed(ctx, command)
	if err != nil {
		return err
	}
	if end == "" || end == w.endID {
		return storage.NewContractError(errors.New("seed append made no progress"))
	}
	w.endID, w.next = end, w.next+1
	return nil
}

// seedCommandSize measures the activity envelope and converter metadata, not
// just the store's inner append value.
func seedCommandSize(command storage.SeedAppend) (int, error) {
	payload, err := workflowcodec.NewDataConverter().ToPayload(&api.StorageActivityCommand{SeedAppend: &command})
	if err != nil {
		return 0, err
	}
	size := len(payload.Data)
	for key, value := range payload.Metadata {
		size += len(key) + len(value)
	}
	return size, nil
}

func (w workflowSeedWriter) BeginRunSeed(ctx context.Context, declaration storage.SeedDeclaration) (storage.RunSeed, error) {
	result, err := w.r.executeStorageWithRetry(ctx, &api.StorageActivityCommand{SeedBegin: &declaration})
	if err != nil {
		return storage.RunSeed{}, err
	}
	return *result.SeedBegin, nil
}

func (w workflowSeedWriter) AppendRunSeed(ctx context.Context, command storage.SeedAppend) (string, error) {
	result, err := w.r.executeStorageWithRetry(ctx, &api.StorageActivityCommand{SeedAppend: &command})
	if err != nil {
		return "", err
	}
	return result.SeedAppend.EndID, nil
}

func (w workflowSeedWriter) PublishRunSeed(ctx context.Context, publication storage.SeedPublication) error {
	_, err := w.r.executeStorageWithRetry(ctx, &api.StorageActivityCommand{SeedPublish: &publication})
	return err
}

func validateSeedPromptFacts(sessionID string, events []prompt.RenderEvent) error {
	for index, event := range events {
		if event.PromptID == "" || event.Version == "" {
			return fmt.Errorf("rendered prompt %d requires prompt id and version", index)
		}
		if event.Scope.SessionID != "" && event.Scope.SessionID != sessionID {
			return fmt.Errorf("rendered prompt %d scope session does not match run session", index)
		}
	}
	return nil
}
