// Public response codecs reject duplicate keys before a map can discard one
// answer and change which value belongs to the saved request.
package runtime

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genruninput "goa.design/goa-ai/registry/gen/runinput"
)

func TestInputResponseCodecRejectsDuplicateAnswerAndCorrelationKeys(t *testing.T) {
	value := &genruninput.InputResponse{Answer: genruninput.NewRunInputAnswerMcp(map[string][]byte{"server-question": []byte(`{"action":"decline"}`)})}
	encoded, err := genruninput.EncodeInputResponse(value)
	require.NoError(t, err)
	_, err = genruninput.DecodeInputResponse(encoded)
	require.NoError(t, err)
	duplicateAnswer := bytes.Replace(encoded, []byte(`"answer":`), []byte(`"answer":null,"answer":`), 1)
	require.NotEqual(t, encoded, duplicateAnswer)
	_, err = genruninput.DecodeInputResponse(duplicateAnswer)
	assert.ErrorContains(t, err, "duplicate") //nolint:testifylint // The duplicate-question test uses the original encoded input independently of this rejection.
	duplicateQuestion := bytes.Replace(encoded, []byte(`"server-question":`), []byte(`"server-question":"e30=","server-question":`), 1)
	require.NotEqual(t, encoded, duplicateQuestion)
	_, err = genruninput.DecodeInputResponse(duplicateQuestion)
	assert.ErrorContains(t, err, "duplicate")
}

func TestRequestedInputCodecRejectsNullQuestionOption(t *testing.T) {
	value := &genruninput.RequestedInput{Request: genruninput.NewRunInputRequestQuestions(&genruninput.InputQuestions{
		Questions:      []*genruninput.InputQuestion{{ID: "question", Prompt: "Choose", Options: []*genruninput.InputQuestionOption{{ID: "one", Label: "One"}}, AllowMultiple: true}},
		ResultContract: &genruninput.InputResultContract{},
	})}
	encoded, err := genruninput.EncodeRequestedInput(value)
	require.NoError(t, err)
	decoded, err := genruninput.DecodeRequestedInput(encoded)
	require.NoError(t, err)
	assert.Equal(t, value, decoded)
	invalid := bytes.Replace(encoded, []byte(`"options":[`), []byte(`"options":[null,`), 1)
	require.NotEqual(t, encoded, invalid)
	_, err = genruninput.DecodeRequestedInput(invalid)
	assert.Error(t, err)
}
