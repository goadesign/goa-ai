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
)
