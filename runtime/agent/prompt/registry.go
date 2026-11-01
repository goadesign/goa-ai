// Package prompt provides prompt template registry management, scoped override
// resolution, sub-prompt rendering composition, and render event publication.
package prompt

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"sync"
	"text/template"

	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// RenderEvent is emitted by Registry after successful prompt rendering.
	RenderEvent struct {
		// PromptID identifies the prompt whose rendered text was consumed.
		PromptID Ident
		// Version identifies the exact baseline or override content that rendered.
		Version string
		// Scope contains the values used to choose a stored prompt override.
		Scope Scope
	}

	// Registry keeps immutable baseline prompt specs and resolves runtime content
	// by layering scoped store overrides on top.
	Registry struct {
		mu    sync.RWMutex
		specs map[Ident]PromptSpec
		store Store
	}

	renderStackKey struct{}
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
	content, events, err := r.render(ctx, id, scope, data)
	if err != nil {
		return nil, err
	}
	for _, event := range events {
		r.publishRender(ctx, event)
	}
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

// Spec returns the registered baseline prompt spec for id, or false if not found.
func (r *Registry) Spec(id Ident) (PromptSpec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	spec, ok := r.specs[id]
	if !ok {
		return PromptSpec{}, false
	}
	return clonePromptSpec(spec), true
}

// ValidateOverride checks that templateSource is valid for the prompt registered with id,
// verifying syntax and checking field accesses against spec.Fields.
func (r *Registry) ValidateOverride(id Ident, templateSource string) error {
	if id == "" {
		return fmt.Errorf("%w: id is required", ErrPromptNotFound)
	}
	if templateSource == "" {
		return fmt.Errorf("%w: template is required", ErrInvalidPromptSpec)
	}
	spec, err := r.lookupSpec(id)
	if err != nil {
		return err
	}
	return validatePromptTemplate(spec, templateSource)
}

// SetOverride validates and persists a prompt override in the configured store.
func (r *Registry) SetOverride(ctx context.Context, id Ident, scope Scope, templateSource string, metadata map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.store == nil {
		return ErrPromptStoreNotConfigured
	}
	if err := r.ValidateOverride(id, templateSource); err != nil {
		return err
	}
	return r.store.Set(ctx, id, scope, templateSource, metadata)
}

// ResetOverride destructively removes stored override records matching the exact
// (id, scope) pair in the configured store. Subsequent resolutions for this prompt
// fall back to the next applicable scoped override or baseline spec.
func (r *Registry) ResetOverride(ctx context.Context, id Ident, scope Scope) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.store == nil {
		return ErrPromptStoreNotConfigured
	}
	if _, err := r.lookupSpec(id); err != nil {
		return err
	}
	return r.store.Reset(ctx, id, scope)
}

// Resolve returns the baseline PromptSpec and the active Override (if any) for id and scope.
func (r *Registry) Resolve(ctx context.Context, id Ident, scope Scope) (PromptSpec, *Override, error) {
	if err := ctx.Err(); err != nil {
		return PromptSpec{}, nil, err
	}
	if id == "" {
		return PromptSpec{}, nil, fmt.Errorf("%w: id is required", ErrPromptNotFound)
	}

	spec, err := r.lookupSpec(id)
	if err != nil {
		return PromptSpec{}, nil, err
	}

	if r.store == nil {
		return spec, nil, nil
	}

	override, resolveErr := r.store.Resolve(ctx, id, scope)
	if resolveErr != nil {
		return PromptSpec{}, nil, fmt.Errorf("resolve prompt override %q: %w", id, resolveErr)
	}
	return spec, override, nil
}

// History returns all stored override records for id, newest-first.
func (r *Registry) History(ctx context.Context, id Ident) ([]*Override, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if id == "" {
		return nil, fmt.Errorf("%w: id is required", ErrPromptNotFound)
	}
	if _, err := r.lookupSpec(id); err != nil {
		return nil, err
	}
	if r.store == nil {
		return nil, ErrPromptStoreNotConfigured
	}
	return r.store.History(ctx, id)
}

func renderStackFromContext(ctx context.Context) []Ident {
	if stack, ok := ctx.Value(renderStackKey{}).([]Ident); ok {
		return stack
	}
	return nil
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
	return validatePromptTemplate(spec, spec.Template)
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
	if len(spec.Fields) > 0 {
		cloned.Fields = tools.CloneFieldMetadata(spec.Fields)
	}
	return cloned
}

func (r *Registry) render(ctx context.Context, id Ident, scope Scope, data any) (*PromptContent, []RenderEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if id == "" {
		return nil, nil, fmt.Errorf("%w: id is required", ErrPromptNotFound)
	}

	stack := renderStackFromContext(ctx)
	for _, parentID := range stack {
		if parentID == id {
			return nil, nil, fmt.Errorf("%w: prompt %q recursively renders itself (chain: %v)", ErrTemplateCycle, id, append(stack, id))
		}
	}
	childStack := append(append([]Ident(nil), stack...), id)
	childCtx := context.WithValue(ctx, renderStackKey{}, childStack)

	spec, override, err := r.Resolve(ctx, id, scope)
	if err != nil {
		return nil, nil, err
	}

	templateSource := spec.Template
	version := spec.Version
	if override != nil {
		templateSource = override.Template
		version = override.Version
		if version == "" {
			version = VersionFromTemplate(templateSource)
		}
	}

	rendered, childRefs, childEvents, err := r.renderTemplate(childCtx, spec, templateSource, scope, data)
	if err != nil {
		return nil, nil, err
	}

	rootRef := PromptRef{
		ID:      spec.ID,
		Version: version,
	}
	seen := make(map[PromptRef]struct{}, 1+len(childRefs))
	allRefs := make([]PromptRef, 0, 1+len(childRefs))
	seen[rootRef] = struct{}{}
	allRefs = append(allRefs, rootRef)
	for _, ref := range childRefs {
		if _, exists := seen[ref]; exists {
			continue
		}
		seen[ref] = struct{}{}
		allRefs = append(allRefs, ref)
	}

	content := &PromptContent{
		Text: rendered,
		Ref:  rootRef,
		Refs: allRefs,
	}
	childEvents = append(childEvents, RenderEvent{
		PromptID: spec.ID,
		Version:  version,
		Scope:    scope,
	})
	return content, childEvents, nil
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

// publishRender records the resolved prompt when the caller supplied a recorder.
func (r *Registry) publishRender(ctx context.Context, event RenderEvent) {
	if recorder := renderRecorderFromContext(ctx); recorder != nil {
		recorder.record(event)
	}
}

// renderTemplate parses and executes one prompt template, injecting sub-prompt rendering.
func (r *Registry) renderTemplate(ctx context.Context, spec PromptSpec, source string, scope Scope, data any) (string, []PromptRef, []RenderEvent, error) {
	var (
		childRefs   []PromptRef
		childEvents []RenderEvent
	)
	tmpl := template.New(spec.ID.String()).Option("missingkey=error")
	funcs := template.FuncMap{
		"render": func(subID string, subData ...any) (string, error) {
			if subID == "" {
				return "", fmt.Errorf("%w: sub-prompt id is required", ErrPromptNotFound)
			}
			arg := data
			if len(subData) > 0 {
				arg = subData[0]
			}
			subContent, subEvents, err := r.render(ctx, Ident(subID), scope, arg)
			if err != nil {
				return "", err
			}
			childRefs = append(childRefs, subContent.Refs...)
			childEvents = append(childEvents, subEvents...)
			return subContent.Text, nil
		},
	}
	for key, value := range spec.Funcs {
		funcs[key] = value
	}
	tmpl = tmpl.Funcs(funcs)

	parsed, err := tmpl.Parse(source)
	if err != nil {
		return "", nil, nil, fmt.Errorf("%w: %s: %w", ErrTemplateParse, spec.ID, err)
	}

	var buffer bytes.Buffer
	if err := parsed.Execute(&buffer, data); err != nil {
		return "", nil, nil, fmt.Errorf("%w: %s: %w", ErrTemplateExecute, spec.ID, err)
	}
	return buffer.String(), childRefs, childEvents, nil
}
