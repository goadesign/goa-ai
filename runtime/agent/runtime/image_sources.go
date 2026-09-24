// Package runtime admits generated image-source contracts independently of tool
// visibility. Saved messages retain typed descriptors; only model request
// preparation calls the current run's host reader. Registration, result
// recording and historical decoding never read image bytes.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolserverdata"
)

type (
	imageSourceRegistry struct {
		producers map[tools.Ident]map[string]tools.ServerDataSpec
		kinds     map[string]tools.ServerDataSpec
		resolve   func(context.Context, run.Context, model.ImageSourcePart, int64) (model.ImagePart, error)
	}

	// imageSourceReaderKey carries only a current-run reader to the built-in
	// summary client. It contains no chosen images, cached bytes or history.
	imageSourceReaderKey struct{}
)

// WithImageSourceResolver admits generated producer/source contracts and binds
// their reader to the current run. Use generated NativeImageSources factories.
// Keep historical kinds registered while saved or copied messages use them;
// their original tools need not be registered or advertised. This option adds
// no tools or resource permissions. resolve must verify current authorization
// and the descriptor's immutable byte identity on every use, within maxPartBytes.
// Invalid or conflicting declarations panic during construction.
func WithImageSourceResolver(contracts map[tools.Ident][]tools.ServerDataSpec, resolve func(context.Context, run.Context, model.ImageSourcePart, int64) (model.ImagePart, error)) RuntimeOption {
	if resolve == nil || len(contracts) == 0 {
		panic("runtime: image source contracts and reader are required")
	}
	return func(opts *Options) {
		if opts.imageSources != nil {
			panic("runtime: image source contracts are already configured")
		}
		registry := &imageSourceRegistry{
			producers: make(map[tools.Ident]map[string]tools.ServerDataSpec, len(contracts)),
			kinds:     make(map[string]tools.ServerDataSpec),
			resolve:   resolve,
		}
		// Sort producer identities so the first owner of an equivalent codec is
		// deterministic. Function addresses cannot identify captured behavior.
		names := make([]string, 0, len(contracts))
		for producer := range contracts {
			names = append(names, producer.String())
		}
		slices.Sort(names)
		for _, name := range names {
			producer := tools.Ident(name)
			specs := contracts[producer]
			if producer == "" || len(specs) == 0 {
				panic("runtime: each image source producer requires a name and contracts")
			}
			entries := make(map[string]tools.ServerDataSpec, len(specs))
			for _, spec := range specs {
				if spec.Kind == "" || !spec.NativeImage || spec.Audience != "evidence" ||
					len(spec.Type.Schema) == 0 || spec.Type.Codec.FromJSON == nil || spec.Type.Codec.ToJSON == nil {
					panic(fmt.Sprintf("runtime: producer %q has an incomplete native image source contract", producer))
				}
				if _, exists := entries[spec.Kind]; exists {
					panic(fmt.Sprintf("runtime: producer %q repeats image source kind %q", producer, spec.Kind))
				}
				spec.Type = cloneTypeSpec(spec.Type)
				if prior, exists := registry.kinds[spec.Kind]; exists {
					if !equivalentImageSourceKind(prior, spec) {
						panic(fmt.Sprintf("runtime: image source kind %q has conflicting contracts", spec.Kind))
					}
				} else {
					registry.kinds[spec.Kind] = spec
				}
				entries[spec.Kind] = spec
			}
			registry.producers[producer] = entries
		}
		opts.imageSources = registry
	}
}

// equivalentImageSource compares a producer's full generated declaration,
// including its Go type name. Function addresses do not identify codec behavior.
func equivalentImageSource(left, right tools.ServerDataSpec) bool {
	left.Type = cloneTypeSpec(left.Type)
	right.Type = cloneTypeSpec(right.Type)
	left.Type.Codec = tools.JSONCodec[any]{}
	right.Type.Codec = tools.JSONCodec[any]{}
	return reflect.DeepEqual(left, right)
}

// equivalentImageSourceKind compares the contract saved as SourceKind and Data.
// A tool-specific top-level Go alias is not saved, so it does not distinguish
// otherwise identical schemas. All other declaration metadata stays exact.
func equivalentImageSourceKind(left, right tools.ServerDataSpec) bool {
	left.Type.Name, right.Type.Name = "", ""
	return equivalentImageSource(left, right)
}

func (r *Runtime) validateImageSourceProducer(spec tools.ToolSpec) error {
	for _, source := range spec.ServerData {
		if !source.NativeImage {
			continue
		}
		if r.imageSources == nil {
			return fmt.Errorf("runtime: native image producer %q is not admitted", spec.Name)
		}
		admitted, ok := r.imageSources.producers[spec.Name][source.Kind]
		if !ok || !equivalentImageSource(admitted, *source) {
			return fmt.Errorf("runtime: native image producer %q kind %q does not match its admitted contract", spec.Name, source.Kind)
		}
	}
	return nil
}

// validateHistoricalImageSources uses retained kind codecs even after the
// producing tool has left the catalog. It performs no current-access or byte read.
func (r *Runtime) validateHistoricalImageSources(messages []*model.Message) error {
	for _, message := range messages {
		for _, part := range message.Parts {
			source, ok := part.(model.ImageSourcePart)
			if !ok {
				continue
			}
			if message.Role != model.ConversationRoleUser {
				return errors.New("runtime: image source requires user role")
			}
			if r.imageSources == nil {
				return fmt.Errorf("runtime: image source kind %q is not registered", source.SourceKind)
			}
			spec, ok := r.imageSources.kinds[source.SourceKind]
			if !ok {
				return fmt.Errorf("runtime: image source kind %q is not registered", source.SourceKind)
			}
			if _, err := spec.Type.Codec.FromJSON(source.Data); err != nil {
				return fmt.Errorf("runtime: image source %q: %w", source.SourceKind, err)
			}
		}
	}
	return nil
}

// toolImageSources copies marked canonical server data into request-only parts.
// The existing envelope decoder owns item fields; semantic result JSON is never
// inspected for descriptors or private proof fields.
func (r *Runtime) toolImageSources(spec tools.ToolSpec, data rawjson.Message) ([]model.Part, error) {
	if err := r.validateImageSourceProducer(spec); err != nil {
		return nil, err
	}
	var marked bool
	for _, source := range spec.ServerData {
		marked = marked || source.NativeImage
	}
	if !marked || len(data) == 0 {
		return nil, nil
	}
	canonical, err := toolserverdata.Apply(spec.CanonicalizeServerData, data)
	if err != nil {
		return nil, err
	}
	var parts []model.Part
	_, err = toolserverdata.Canonicalize(canonical, func(kind, audience string, data rawjson.Message) (string, rawjson.Message, error) {
		// Historical admission preserves old decoders. Only this selected
		// tool declaration can mark a present result item as a new image.
		for _, source := range spec.ServerData {
			if source.Kind == kind && source.NativeImage {
				parts = append(parts, model.ImageSourcePart{SourceKind: kind, Data: slices.Clone(data)})
				break
			}
		}
		return audience, data, nil
	})
	return parts, err
}

// imageSourceClient binds the current run once, then validates each frozen
// request before history selection. Its context also supplies the same reader
// to summaries, without recursively applying the outer history policy.
func (r *Runtime) imageSourceClient(client model.Client, current run.Context) model.Client {
	if r.imageSources == nil {
		return client
	}
	resolve := model.ImageSourceResolver(func(ctx context.Context, source model.ImageSourcePart, maxPartBytes int64) (model.ImagePart, error) {
		if err := r.validateHistoricalImageSources([]*model.Message{{
			Role: model.ConversationRoleUser, Parts: []model.Part{source},
		}}); err != nil {
			return model.ImagePart{}, err
		}
		return r.imageSources.resolve(ctx, current, source, maxPartBytes)
	})
	client, err := model.WithImageSourceResolver(client, resolve)
	if err != nil {
		panic(err)
	}
	client, err = model.WithRequestPreparation(client, func(ctx context.Context, request *model.Request) (context.Context, *model.Request, error) {
		if err := r.validateHistoricalImageSources(request.Messages); err != nil {
			return ctx, nil, err
		}
		return context.WithValue(ctx, imageSourceReaderKey{}, resolve), request, nil
	})
	if err != nil {
		panic(err)
	}
	return client
}
