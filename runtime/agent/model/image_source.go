// Package model replaces retained image descriptors only for concrete count or
// inference requests. It preserves the caller's saved messages and charges every
// returned image occurrence to the existing request-wide allocation allowance.
package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// ImageSourceResolver verifies current access and returns the exact image
// described by source. maxPartBytes includes both Format and Bytes. The reader
// must enforce this allowance before expensive reads and honor ctx's deadline.
// It must not replace a missing or changed source with another image.
type ImageSourceResolver func(ctx context.Context, source ImageSourcePart, maxPartBytes int64) (ImagePart, error)

// ErrImageSourceCapacity means a concrete candidate's known byte allowance
// cannot contain its selected image. Readers must not use it for authorization,
// identity, missing-object, transport, token or unknown provider-capacity errors.
var ErrImageSourceCapacity = errors.New("model image source exceeds candidate byte allowance")

// WithImageSourceResolver installs a reader after ordinary request preparations.
// CountTokens uses the same reader without running those preparations. Each use
// checks current access; bytes are never cached between candidates or calls.
// A client that already has a reader rejects another binding, including the
// same function. Bind each execution from an unbound base client.
func WithImageSourceResolver(client Client, resolve ImageSourceResolver) (Client, error) {
	core, err := validatedClientCore(client)
	if err != nil {
		return nil, err
	}
	if core.imageSourceResolver() != nil {
		return nil, errors.New("model image source resolver is already bound")
	}
	if resolve == nil {
		return nil, errors.New("model image source resolver is required")
	}
	counter, _ := core.rawProvider().(TokenCounter)
	client, err = newValidatedClient(core.rawProvider(), counter, slices.Clone(core.callObservers()), slices.Clone(core.requestPreparations())...)
	if err != nil {
		return nil, err
	}
	client.(*validatedClient).imageResolver = resolve
	return client, nil
}

func validateImageSource(source ImageSourcePart) error {
	if source.SourceKind == "" {
		return errors.New("ImageSourcePart requires source_kind")
	}
	if !json.Valid(source.Data) || bytes.Equal(bytes.TrimSpace(source.Data), []byte("null")) {
		return errors.New("ImageSourcePart requires a non-null JSON descriptor")
	}
	return nil
}

func rejectImageSources(request *Request) error {
	for _, message := range request.Messages {
		for _, part := range message.Parts {
			if _, ok := part.(ImageSourcePart); ok {
				return errors.New("model provider request contains an unresolved image source")
			}
		}
	}
	return nil
}

// resolveImageSources mutates only the request already owned by this client.
// Descriptor charges are replaced by actual image charges. All fixed input,
// including images appearing later in the request, is counted before any read.
func resolveImageSources(ctx context.Context, request *Request, resolve ImageSourceResolver) error {
	var sources, descriptorBytes int
	for _, message := range request.Messages {
		for _, part := range message.Parts {
			if source, ok := part.(ImageSourcePart); ok {
				sources++
				descriptorBytes += len(source.SourceKind) + len(source.Data)
			}
		}
	}
	if sources == 0 {
		return nil
	}
	walk := &dynamicValueWalk{}
	if err := preflightRequestWithWalk(request, walk); err != nil {
		return err
	}
	walk.bytes -= descriptorBytes
	if resolve == nil {
		return errors.New("model image source resolver is not configured")
	}
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("model image source reading requires a finite context deadline")
	}
	for messageIndex, message := range request.Messages {
		for partIndex, part := range message.Parts {
			source, ok := part.(ImageSourcePart)
			if !ok {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			remaining := maxDynamicValueBytes - walk.bytes
			if remaining <= 0 {
				return ErrImageSourceCapacity
			}
			// The callback cannot mutate saved descriptors or another candidate.
			source.Data = slices.Clone(source.Data)
			image, err := resolve(ctx, source, int64(remaining))
			if err != nil {
				return fmt.Errorf("model message %d part %d image source: %w", messageIndex, partIndex, err)
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if len(image.Format) > remaining || len(image.Bytes) > remaining-len(image.Format) {
				return errors.New("model image source reader violated its byte allowance")
			}
			if image.Format == "" || len(image.Bytes) == 0 {
				return errors.New("model image source reader returned an image without format or bytes")
			}
			if err := chargeString(walk, string(image.Format)); err != nil {
				return err
			}
			if err := walk.addBytes(len(image.Bytes)); err != nil {
				return err
			}
			image.Bytes = slices.Clone(image.Bytes)
			message.Parts[partIndex] = image
		}
	}
	return nil
}
