package prompt

import "errors"

var (
	// ErrPromptNotFound reports that no baseline prompt exists for the requested ID.
	ErrPromptNotFound = errors.New("prompt not found")
	// ErrDuplicatePromptSpec reports that a prompt ID has already been registered.
	ErrDuplicatePromptSpec = errors.New("duplicate prompt spec")
	// ErrTemplateParse reports that a resolved prompt template failed to parse.
	ErrTemplateParse = errors.New("prompt template parse failed")
	// ErrTemplateExecute reports that a resolved prompt template failed to execute.
	ErrTemplateExecute = errors.New("prompt template execute failed")
	// ErrInvalidPromptSpec reports that a prompt spec violates required contracts.
	ErrInvalidPromptSpec = errors.New("invalid prompt spec")
	// ErrTemplateFieldNotFound reports that a prompt template references an undeclared field.
	ErrTemplateFieldNotFound = errors.New("prompt template references undeclared field")
	// ErrPromptStoreNotConfigured reports that an override operation was attempted on a registry without a store.
	ErrPromptStoreNotConfigured = errors.New("prompt override store is not configured")
	// ErrTemplateCycle reports a recursive prompt render cycle.
	ErrTemplateCycle = errors.New("prompt render cycle detected")
)
