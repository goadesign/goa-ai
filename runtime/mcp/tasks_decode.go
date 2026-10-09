// Package mcp checks the Tasks extension's external JSON contract before returning
// a typed observation. Required nullable retention is checked here because Goa's
// native required scalar validation does not distinguish present null from absence.
package mcp

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent/rawjson"
)

// decodeTaskInfo reads exactly the metadata members owned by the Tasks extension.
// It preserves null retention, exact integer durations and original timestamp strings;
// absent or wrongly typed required members never become zero-valued metadata.
func decodeTaskInfo(data []byte) (TaskInfo, error) {
	var fields map[string]json.RawMessage
	if err := jsonv2.Unmarshal(data, &fields); err != nil || fields == nil {
		return TaskInfo{}, errors.New("task must be a JSON object")
	}
	if err := validateMeta(fields["_meta"]); err != nil {
		return TaskInfo{}, err
	}
	var info TaskInfo
	for _, field := range []struct {
		name   string
		target *string
	}{
		{"taskId", &info.TaskID}, {"createdAt", &info.CreatedAt}, {"lastUpdatedAt", &info.LastUpdatedAt},
	} {
		value, err := requiredStringField(fields, field.name)
		if err != nil {
			return TaskInfo{}, err
		}
		*field.target = *value
	}
	status, err := requiredStringField(fields, "status")
	if err != nil {
		return TaskInfo{}, err
	}
	info.Status = TaskStatus(*status)
	switch info.Status {
	case TaskWorking, TaskInputRequired, TaskCompleted, TaskFailed, TaskCancelled:
	default:
		return TaskInfo{}, fmt.Errorf("unsupported task status %q", info.Status)
	}
	if _, ok := fields["statusMessage"]; ok {
		value, err := requiredStringField(fields, "statusMessage")
		if err != nil {
			return TaskInfo{}, err
		}
		info.StatusMessage = value
	}
	ttl, present := fields["ttlMs"]
	if !present {
		return TaskInfo{}, errors.New("task ttlMs is required, including when null")
	}
	if !bytes.Equal(bytes.TrimSpace(ttl), []byte("null")) {
		value, err := rawjson.DecodeInteger[int64](ttl)
		if err != nil {
			return TaskInfo{}, fmt.Errorf("task ttlMs: %w", err)
		}
		info.TTLMs = &value
	}
	if poll, present := fields["pollIntervalMs"]; present {
		value, err := rawjson.DecodeInteger[int64](poll)
		if err != nil {
			return TaskInfo{}, fmt.Errorf("task pollIntervalMs: %w", err)
		}
		info.PollIntervalMs = &value
	}
	return info, nil
}

// UnmarshalJSON checks the tasks/get result discriminator before decoding its
// detailed state. Subscription notifications reuse the same state decoder.
func (r *taskGetResult) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := jsonv2.Unmarshal(data, &fields); err != nil {
		return err
	}
	resultType, err := requiredStringField(fields, "resultType")
	if err != nil || *resultType != resultComplete {
		return errors.New("tasks/get requires resultType complete")
	}
	task, err := decodeDetailedTask(data)
	if err != nil {
		return err
	}
	r.task = task
	return nil
}

// decodeDetailedTask selects the reported status before decoding its data.
// Queries and notifications therefore enforce the same result and input rules.
func decodeDetailedTask(data []byte) (Task, error) {
	info, err := decodeTaskInfo(data)
	if err != nil {
		return Task{}, err
	}
	var fields map[string]json.RawMessage
	if err := jsonv2.Unmarshal(data, &fields); err != nil {
		return Task{}, err
	}
	task := Task{info: info}
	switch info.Status {
	case TaskWorking, TaskCancelled:
		// These states report metadata without input, a result or an error.
	case TaskInputRequired:
		var requests map[string]InputRequest
		if err := jsonv2.Unmarshal(fields["inputRequests"], &requests); err != nil || requests == nil {
			return Task{}, errors.New("input_required task requires an inputRequests object")
		}
		task.input = &InputRequired{Requests: requests}
	case TaskCompleted:
		var result toolsCallResult
		if err := jsonv2.Unmarshal(fields["result"], &result); err != nil {
			return Task{}, fmt.Errorf("completed task result: %w", err)
		}
		if result.ResultType != resultComplete {
			return Task{}, errors.New("completed task must contain a complete tool result")
		}
		response, err := completedTaskResult(result)
		if err != nil {
			return Task{}, err
		}
		task.result = &response
	case TaskFailed:
		message := rpcMessage{Error: fields["error"]}
		if len(message.Error) == 0 {
			return Task{}, errors.New("failed task requires a JSON-RPC error")
		}
		failure, err := message.responseError()
		if err != nil {
			return Task{}, err
		}
		task.failure = failure.callerError()
	}
	return task, nil
}

// UnmarshalJSON requires the acknowledgement's exact complete discriminator;
// null and differently named members cannot become a successful acknowledgement.
func (r *taskAcknowledgment) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := jsonv2.Unmarshal(data, &fields); err != nil || fields == nil {
		return errors.New("task acknowledgement must be an object")
	}
	resultType, err := requiredStringField(fields, "resultType")
	if err != nil {
		return err
	}
	*r = taskAcknowledgment{ResultType: *resultType, Meta: fields["_meta"]}
	return nil
}
