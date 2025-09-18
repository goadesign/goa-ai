package assistantapi

import (
	"context"

	websocketsvc "example.com/assistant/gen/websocket"
	"goa.design/clue/log"
)

// websocket service example implementation.
// The example methods log the requests and return zero values.
type websocketsrvc struct{}

// NewWebsocket returns the websocket service implementation.
func NewWebsocket() websocketsvc.Service {
	return &websocketsrvc{}
}

// Upload data chunks via client stream
func (s *websocketsrvc) UploadChunks(ctx context.Context, stream websocketsvc.UploadChunksServerStream) (err error) {
	log.Printf(ctx, "websocket.upload_chunks")
	return
}

// Upload multiple documents via client stream
func (s *websocketsrvc) UploadDocuments(ctx context.Context, stream websocketsvc.UploadDocumentsServerStream) (err error) {
	log.Printf(ctx, "websocket.upload_documents")
	return
}

// Interactive chat with bidirectional streaming
func (s *websocketsrvc) Chat(ctx context.Context, stream websocketsvc.ChatServerStream) (err error) {
	log.Printf(ctx, "websocket.chat")
	return
}

// Extended interactive chat with bidirectional streaming
func (s *websocketsrvc) InteractiveChat(ctx context.Context, stream websocketsvc.InteractiveChatServerStream) (err error) {
	log.Printf(ctx, "websocket.interactive_chat")
	return
}
