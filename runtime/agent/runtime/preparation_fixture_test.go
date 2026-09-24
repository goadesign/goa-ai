package runtime

import (
	"context"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/storage"
)

// publishLiteralHistory serves deterministic callback and direct workflow
// callers. Their supplied messages are already fixed; the publication retains
// an explicit completion value along with the history.
func publishLiteralHistory(ctx context.Context, store seedWriter, declaration storage.SeedDeclaration, messages []*model.Message) (string, error) {
	declaration.CommandID, declaration.AttemptID = declaration.RunID, declaration.RunID
	writer, err := stageLiteralHistory(ctx, store, declaration, messages)
	if err != nil {
		return "", err
	}
	end := writer.endID
	if err := writer.publish(ctx, []byte(`{}`)); err != nil {
		return "", err
	}
	return end, nil
}
