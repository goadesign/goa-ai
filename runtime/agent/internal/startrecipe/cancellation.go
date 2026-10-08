// Package startrecipe binds each cancellation job to its original run and exact
// routing and reason. Engines use this identity before accepting duplicate jobs.
package startrecipe

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"goa.design/goa-ai/runtime/agent/api"
)

// CancellationRecipe validates one engine job request and returns its stable ID
// and exact request digest. A different reason keeps the ID and changes the digest.
func CancellationRecipe(name, queue string, request api.CancellationRequest) (string, [sha256.Size]byte, error) {
	if name == "" || queue == "" || request.RunID == "" || request.Reason == "" {
		return "", [sha256.Size]byte{}, errors.New("cancellation job requires workflow name, queue, run id and reason")
	}
	if !utf8.ValidString(name) || !utf8.ValidString(queue) || !utf8.ValidString(request.RunID) || !utf8.ValidString(request.Reason) {
		return "", [sha256.Size]byte{}, errors.New("cancellation job contains invalid UTF-8")
	}
	data, err := json.Marshal(struct {
		Name    string
		Queue   string
		Request api.CancellationRequest
	}{Name: name, Queue: queue, Request: request})
	if err != nil {
		return "", [sha256.Size]byte{}, err
	}
	identity := sha256.Sum256([]byte(request.RunID))
	return "goa-ai-cancel/" + hex.EncodeToString(identity[:]), sha256.Sum256(data), nil
}
