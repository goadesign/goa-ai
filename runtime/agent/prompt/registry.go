package prompt

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"sync"
	"text/template"
)

type (
	// RenderEvent is emitted by Registry after successful prompt rendering.
	RenderEvent struct {
		PromptID Ident
		Version  string
		Scope    Scope
	}

	// RenderObserver receives prompt render events.
	RenderObserver func(ctx context.Context, event RenderEvent)

	// Registry keeps immutable baseline prompt specs and resolves runtime content
	// by layering scoped store overrides on top.
	Registry struct {
		mu       sync.RWMutex
		specs    map[Ident]PromptSpec
		store    Store
		observer RenderObserver
	}
)

// NewRegistry returns an empty prompt registry.
func NewRegistry(store Store) *Registry {
	return &Registry{
		specs: make(map[Ident]PromptSpec),
		store: store,
	}
}

// Register adds one baseline prompt spec.
func (r *Registry) Register(spec PromptSpec) error {
	if err := validatePromptSpec(spec); err != nil {
		return err
	}

	specCopy := clonePromptSpec(spec)
	if specCopy.Version == "" {
		specCopy.Version = VersionFromTemplate(specCopy.Template)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.specs[specCopy.ID]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicatePromptSpec, specCopy.ID)
	}
	r.specs[specCopy.ID] = specCopy
	return nil
}

// Render resolves and renders one prompt using baseline+override composition.
func (r *Registry) Render(ctx context.Context, id Ident, scope Scope, data any) (*PromptContent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if id == "" {
		return nil, fmt.Errorf("%w: id is required", ErrPromptNotFound)
	}

	spec, err := r.lookupSpec(id)
	if err != nil {
		return nil, err
	}

	templateSource := spec.Template
	version := spec.Version
	if r.store != nil {
		override, resolveErr := r.store.Resolve(ctx, id, scope)
		if resolveErr != nil {
			return nil, fmt.Errorf("resolve prompt override %q: %w", id, resolveErr)
		}
		if override != nil {
			templateSource = override.Template
			version = override.Version
			if version == "" {
				version = VersionFromTemplate(templateSource)
			}
		}
	}

	rendered, err := renderTemplate(spec, templateSource, data)
	if err != nil {
		return nil, err
	}

	content := &PromptContent{
		Text: rendered,
		Ref: PromptRef{
			ID:      spec.ID,
			Version: version,
		},
	}
	r.publishRender(ctx, RenderEvent{
		PromptID: spec.ID,
		Version:  version,
		Scope:    scope,
	})
	return content, nil
}

// List returns all registered prompt specs sorted by ID.
func (r *Registry) List() []PromptSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := make([]Ident, 0, len(r.specs))
	for id := range r.specs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return ids[i] < ids[j]
	})

	specs := make([]PromptSpec, 0, len(ids))
	for _, id := range ids {
		specs = append(specs, clonePromptSpec(r.specs[id]))
	}
	return specs
}

// SetObserver configures the observer for successful render events.
func (r *Registry) SetObserver(observer RenderObserver) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.observer = observer
}

// SetStore updates the override store used by Render.
func (r *Registry) SetStore(store Store) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.store = store
}

// lookupSpec resolves one prompt spec by ID.
func (r *Registry) lookupSpec(id Ident) (PromptSpec, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	spec, ok := r.specs[id]
	if !ok {
		return PromptSpec{}, fmt.Errorf("%w: %s", ErrPromptNotFound, id)
	}
	return clonePromptSpec(spec), nil
}

// publishRender emits render events if an observer is configured.
func (r *Registry) publishRender(ctx context.Context, event RenderEvent) {
	r.mu.RLock()
	observer := r.observer
	r.mu.RUnlock()
	if observer == nil {
		return
	}
	observer(ctx, event)
}

// renderTemplate parses and executes one prompt template.
func renderTemplate(spec PromptSpec, source string, data any) (string, error) {
	tmpl := template.New(spec.ID.String()).Option("missingkey=error")
	if len(spec.Funcs) > 0 {
		tmpl = tmpl.Funcs(spec.Funcs)
	}
	parsed, err := tmpl.Parse(source)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrTemplateParse, spec.ID, err)
	}

	var buffer bytes.Buffer
	if err := parsed.Execute(&buffer, data); err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrTemplateExecute, spec.ID, err)
	}
	return buffer.String(), nil
}

// validatePromptSpec enforces baseline prompt registration requirements.
func validatePromptSpec(spec PromptSpec) error {
	if spec.ID == "" {
		return fmt.Errorf("%w: id is required", ErrInvalidPromptSpec)
	}
	if spec.AgentID == "" {
		return fmt.Errorf("%w: agent_id is required", ErrInvalidPromptSpec)
	}
	if spec.Role == "" {
		return fmt.Errorf("%w: role is required", ErrInvalidPromptSpec)
	}
	if spec.Template == "" {
		return fmt.Errorf("%w: template is required", ErrInvalidPromptSpec)
	}
	return nil
}

// clonePromptSpec returns a safe copy of a prompt spec.
func clonePromptSpec(spec PromptSpec) PromptSpec {
	cloned := spec
	if spec.Funcs != nil {
		cloned.Funcs = make(template.FuncMap, len(spec.Funcs))
		for key, value := range spec.Funcs {
			cloned.Funcs[key] = value
		}
	}
	return cloned
}
