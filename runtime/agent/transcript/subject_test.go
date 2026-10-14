// These tests distinguish actual Agent model history from service-tool records.
// Invalid saved subjects must fail before a transcript reader asks for messages.
package transcript_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/transcript"
)

type subjectPrefixStore struct {
	prefixPageFunc
	subject genrunidentity.RunSubject
}

func (s subjectPrefixStore) LoadRun(ctx context.Context, runID string) (session.RunMeta, error) {
	return session.RunMeta{RunID: runID, Subject: s.subject}, ctx.Err()
}

func TestTranscriptRejectsServiceSubjectBeforeReadingMessages(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		subject genrunidentity.RunSubject
		want    string
	}{
		{name: "absent subject", want: "invalid transcript subject"},
		{name: "service subject", subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "records.lookup"})}, want: "transcript history requires an Agent subject"},
	} {
		t.Run(test.name, func(t *testing.T) {
			reads := 0
			store := subjectPrefixStore{
				subject: test.subject,
				prefixPageFunc: prefixPageFunc(func(context.Context, string, string, string, int) (runlog.Page, error) {
					reads++
					return runlog.Page{}, nil
				}),
			}
			_, err := transcript.BuildMessagesFromRunLogPrefix(t.Context(), store, "run", "end")
			require.ErrorContains(t, err, test.want)
			assert.Zero(t, reads)
		})
	}
}

func TestTranscriptReplayRejectsServiceMessages(t *testing.T) {
	t.Parallel()
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "records.lookup"})}
	messages, found, err := transcript.ReplayRunLogEvents([]*runlog.Event{{Subject: subject, Type: "run_started"}})
	require.NoError(t, err)
	assert.False(t, found)
	assert.Empty(t, messages)

	_, _, err = transcript.ReplayRunLogEvents([]*runlog.Event{{Subject: subject, Type: transcript.RunLogMessagesAppended, Payload: []byte(`[]`)}})
	assert.ErrorContains(t, err, "transcript history requires an Agent subject")
}
