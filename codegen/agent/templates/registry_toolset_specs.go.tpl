// RegistryClient defines the interface for fetching toolset data from a registry.
type RegistryClient interface {
	GetToolset(ctx context.Context, name string) (*ToolsetSchema, error)
}

// ToolsetSchema represents a toolset fetched from the registry.
type ToolsetSchema struct {
	Name        string
	Description string
	Tools       []ToolSchema
}

// ToolSchema represents a tool definition from the registry.
type ToolSchema struct {
	Name            string
	Title           string
	Description     string
	Tags            []string
	PayloadTypeName string
	PayloadSchema   []byte
	ResultTypeName  string
	ResultSchema    []byte
}

// SchemaValidationError represents a validation error with structured details.
type SchemaValidationError struct {
	Path    string // JSON path to the invalid field
	Message string // Human-readable error message
	Value   any    // The invalid value (may be nil)
}

// SchemaValidationErrors collects multiple validation errors.
type SchemaValidationErrors struct {
	Errors []*SchemaValidationError
}

// RegistryToolsetID is the identifier for this registry-backed toolset.
const RegistryToolsetID = {{ printf "%q" .QualifiedName }}

// RegistryName is the name of the registry source.
const RegistryName = {{ printf "%q" .Registry.RegistryName }}

// ToolsetName is the name of the toolset in the registry.
const ToolsetName = {{ printf "%q" .Registry.ToolsetName }}

{{- if .Registry.Version }}

// Version is the pinned version for this toolset.
const Version = {{ printf "%q" .Registry.Version }}
{{- end }}

// Specs holds the tool specifications discovered from the registry.
// This slice is populated at runtime via DiscoverAndPopulate.
var Specs []tools.ToolSpec

var (
	specIndex = make(map[tools.Ident]*tools.ToolSpec)
	metadata  []policy.ToolMetadata
	mu        sync.RWMutex
)

// DiscoverAndPopulate fetches tool schemas from the registry and populates
// the Specs slice. This function should be called during agent initialization
// before the agent starts processing requests.
//
// The function is safe to call multiple times; subsequent calls will refresh
// the cached specifications.
func DiscoverAndPopulate(ctx context.Context, client RegistryClient) error {
	toolset, err := client.GetToolset(ctx, ToolsetName)
	if err != nil {
		return fmt.Errorf("discover toolset %q from registry %q: %w", ToolsetName, RegistryName, err)
	}
	if toolset == nil {
		return fmt.Errorf("toolset %q not found in registry %q", ToolsetName, RegistryName)
	}

	mu.Lock()
	defer mu.Unlock()

	Specs = make([]tools.ToolSpec, 0, len(toolset.Tools))
	specIndex = make(map[tools.Ident]*tools.ToolSpec, len(toolset.Tools))
	metadata = make([]policy.ToolMetadata, 0, len(toolset.Tools))

	for _, tool := range toolset.Tools {
		spec := tools.ToolSpec{
			Name:        tools.Ident(tool.Name),
			Service:     {{ printf "%q" .ServiceName }},
			Toolset:     ToolsetName,
			Description: tool.Description,
			Tags:        tool.Tags,
			Payload: tools.TypeSpec{
				Name:   tool.PayloadTypeName,
				Schema: tool.PayloadSchema,
				Codec:  tools.JSONCodec[any]{},
			},
			Result: tools.TypeSpec{
				Name:   tool.ResultTypeName,
				Schema: tool.ResultSchema,
				Codec:  tools.JSONCodec[any]{},
			},
		}
		Specs = append(Specs, spec)
		specIndex[spec.Name] = &Specs[len(Specs)-1]
		metadata = append(metadata, policy.ToolMetadata{
			ID:          spec.Name,
			Title:       tool.Title,
			Description: tool.Description,
			Tags:        tool.Tags,
		})
	}

	return nil
}

// Names returns the identifiers of all discovered tools.
func Names() []tools.Ident {
	mu.RLock()
	defer mu.RUnlock()

	names := make([]tools.Ident, 0, len(specIndex))
	for name := range specIndex {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return string(names[i]) < string(names[j])
	})
	return names
}

// Spec returns the specification for the named tool if present.
func Spec(name tools.Ident) (*tools.ToolSpec, bool) {
	mu.RLock()
	defer mu.RUnlock()

	spec, ok := specIndex[name]
	return spec, ok
}

// PayloadSchema returns the JSON schema for the named tool payload.
func PayloadSchema(name tools.Ident) ([]byte, bool) {
	mu.RLock()
	defer mu.RUnlock()

	spec, ok := specIndex[name]
	if !ok {
		return nil, false
	}
	return spec.Payload.Schema, true
}

// ResultSchema returns the JSON schema for the named tool result.
func ResultSchema(name tools.Ident) ([]byte, bool) {
	mu.RLock()
	defer mu.RUnlock()

	spec, ok := specIndex[name]
	if !ok {
		return nil, false
	}
	return spec.Result.Schema, true
}

// Metadata exposes policy metadata for the discovered tools.
func Metadata() []policy.ToolMetadata {
	mu.RLock()
	defer mu.RUnlock()

	out := make([]policy.ToolMetadata, len(metadata))
	copy(out, metadata)
	return out
}

// ValidatePayload validates a tool payload against its registry-provided schema.
// Returns nil if the payload is valid or if no schema is available.
func ValidatePayload(name tools.Ident, payload any) error {
	schema, ok := PayloadSchema(name)
	if !ok || len(schema) == 0 {
		return nil
	}
	return validateAgainstSchema(schema, payload, "payload")
}

// ValidateResult validates a tool result against its registry-provided schema.
// Returns nil if the result is valid or if no schema is available.
func ValidateResult(name tools.Ident, result any) error {
	schema, ok := ResultSchema(name)
	if !ok || len(schema) == 0 {
		return nil
	}
	return validateAgainstSchema(schema, result, "result")
}

func (e *SchemaValidationError) Error() string {
	if e.Path == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Path, e.Message)
}

func (e *SchemaValidationErrors) Error() string {
	if len(e.Errors) == 0 {
		return "validation failed"
	}
	if len(e.Errors) == 1 {
		return e.Errors[0].Error()
	}
	var msgs []string
	for _, err := range e.Errors {
		msgs = append(msgs, err.Error())
	}
	return fmt.Sprintf("validation failed: %s", strings.Join(msgs, "; "))
}

func (e *SchemaValidationErrors) Add(path, message string, value any) {
	e.Errors = append(e.Errors, &SchemaValidationError{
		Path:    path,
		Message: message,
		Value:   value,
	})
}

func (e *SchemaValidationErrors) HasErrors() bool {
	return len(e.Errors) > 0
}

func validateAgainstSchema(schema []byte, data any, context string) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal %s for validation: %w", context, err)
	}

	var schemaObj map[string]any
	if err := json.Unmarshal(schema, &schemaObj); err != nil {
		return fmt.Errorf("parse %s schema: %w", context, err)
	}

	var dataObj any
	if err := json.Unmarshal(jsonData, &dataObj); err != nil {
		return fmt.Errorf("parse %s data: %w", context, err)
	}

	errs := &SchemaValidationErrors{}
	validateType(schemaObj, dataObj, "", errs)

	if errs.HasErrors() {
		return errs
	}
	return nil
}

func validateType(schema map[string]any, data any, path string, errs *SchemaValidationErrors) {
	if data == nil {
		if nullable, ok := schema["nullable"].(bool); ok && nullable {
			return
		}
		schemaType, _ := schema["type"].(string)
		if schemaType != "" && schemaType != "null" {
			errs.Add(path, fmt.Sprintf("expected %s, got null", schemaType), nil)
		}
		return
	}

	schemaType, _ := schema["type"].(string)

	switch schemaType {
	case "object":
		validateObject(schema, data, path, errs)
	case "array":
		validateArray(schema, data, path, errs)
	case "string":
		validateString(schema, data, path, errs)
	case "number", "integer":
		validateNumber(schema, data, path, schemaType, errs)
	case "boolean":
		if _, ok := data.(bool); !ok {
			errs.Add(path, fmt.Sprintf("expected boolean, got %T", data), data)
		}
	case "null":
		if data != nil {
			errs.Add(path, fmt.Sprintf("expected null, got %T", data), data)
		}
	case "":
		if oneOf, ok := schema["oneOf"].([]any); ok {
			validateOneOf(oneOf, data, path, errs)
		}
		if anyOf, ok := schema["anyOf"].([]any); ok {
			validateAnyOf(anyOf, data, path, errs)
		}
	}
}

func validateObject(schema map[string]any, data any, path string, errs *SchemaValidationErrors) {
	obj, ok := data.(map[string]any)
	if !ok {
		errs.Add(path, fmt.Sprintf("expected object, got %T", data), data)
		return
	}

	if required, ok := schema["required"].([]any); ok {
		for _, r := range required {
			fieldName, _ := r.(string)
			if _, exists := obj[fieldName]; !exists {
				fieldPath := joinPath(path, fieldName)
				errs.Add(fieldPath, "missing required field", nil)
			}
		}
	}

	props, _ := schema["properties"].(map[string]any)
	additionalProps := true
	if ap, ok := schema["additionalProperties"].(bool); ok {
		additionalProps = ap
	}

	for key, val := range obj {
		fieldPath := joinPath(path, key)
		if props != nil {
			if propSchema, ok := props[key].(map[string]any); ok {
				validateType(propSchema, val, fieldPath, errs)
				continue
			}
		}
		if !additionalProps {
			errs.Add(fieldPath, "additional property not allowed", val)
		}
	}
}

func validateArray(schema map[string]any, data any, path string, errs *SchemaValidationErrors) {
	arr, ok := data.([]any)
	if !ok {
		errs.Add(path, fmt.Sprintf("expected array, got %T", data), data)
		return
	}

	if minItems, ok := schema["minItems"].(float64); ok {
		if float64(len(arr)) < minItems {
			errs.Add(path, fmt.Sprintf("array length %d is less than minimum %d", len(arr), int(minItems)), nil)
		}
	}

	if maxItems, ok := schema["maxItems"].(float64); ok {
		if float64(len(arr)) > maxItems {
			errs.Add(path, fmt.Sprintf("array length %d exceeds maximum %d", len(arr), int(maxItems)), nil)
		}
	}

	if items, ok := schema["items"].(map[string]any); ok {
		for i, item := range arr {
			itemPath := fmt.Sprintf("%s[%d]", path, i)
			if path == "" {
				itemPath = fmt.Sprintf("[%d]", i)
			}
			validateType(items, item, itemPath, errs)
		}
	}
}

func validateString(schema map[string]any, data any, path string, errs *SchemaValidationErrors) {
	str, ok := data.(string)
	if !ok {
		errs.Add(path, fmt.Sprintf("expected string, got %T", data), data)
		return
	}

	if minLen, ok := schema["minLength"].(float64); ok {
		if float64(len(str)) < minLen {
			errs.Add(path, fmt.Sprintf("string length %d is less than minimum %d", len(str), int(minLen)), str)
		}
	}

	if maxLen, ok := schema["maxLength"].(float64); ok {
		if float64(len(str)) > maxLen {
			errs.Add(path, fmt.Sprintf("string length %d exceeds maximum %d", len(str), int(maxLen)), str)
		}
	}

	if enum, ok := schema["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			if eStr, ok := e.(string); ok && eStr == str {
				found = true
				break
			}
		}
		if !found {
			errs.Add(path, fmt.Sprintf("value %q is not in enum", str), str)
		}
	}

	if pattern, ok := schema["pattern"].(string); ok {
		matched, err := regexp.MatchString(pattern, str)
		if err != nil {
			errs.Add(path, fmt.Sprintf("invalid pattern %q: %v", pattern, err), str)
		} else if !matched {
			errs.Add(path, fmt.Sprintf("value %q does not match pattern %q", str, pattern), str)
		}
	}
}

func validateNumber(schema map[string]any, data any, path, schemaType string, errs *SchemaValidationErrors) {
	var num float64
	switch v := data.(type) {
	case float64:
		num = v
	case int:
		num = float64(v)
	case int64:
		num = float64(v)
	case float32:
		num = float64(v)
	default:
		errs.Add(path, fmt.Sprintf("expected %s, got %T", schemaType, data), data)
		return
	}

	if schemaType == "integer" {
		if num != float64(int64(num)) {
			errs.Add(path, fmt.Sprintf("expected integer, got %v", num), data)
		}
	}

	if min, ok := schema["minimum"].(float64); ok {
		if num < min {
			errs.Add(path, fmt.Sprintf("value %v is less than minimum %v", num, min), data)
		}
	}

	if max, ok := schema["maximum"].(float64); ok {
		if num > max {
			errs.Add(path, fmt.Sprintf("value %v exceeds maximum %v", num, max), data)
		}
	}

	if exMin, ok := schema["exclusiveMinimum"].(float64); ok {
		if num <= exMin {
			errs.Add(path, fmt.Sprintf("value %v must be greater than %v", num, exMin), data)
		}
	}

	if exMax, ok := schema["exclusiveMaximum"].(float64); ok {
		if num >= exMax {
			errs.Add(path, fmt.Sprintf("value %v must be less than %v", num, exMax), data)
		}
	}
}

func validateOneOf(oneOf []any, data any, path string, errs *SchemaValidationErrors) {
	validCount := 0
	for _, schema := range oneOf {
		schemaMap, ok := schema.(map[string]any)
		if !ok {
			continue
		}
		testErrs := &SchemaValidationErrors{}
		validateType(schemaMap, data, path, testErrs)
		if !testErrs.HasErrors() {
			validCount++
		}
	}
	if validCount != 1 {
		errs.Add(path, fmt.Sprintf("value must match exactly one schema in oneOf, matched %d", validCount), data)
	}
}

func validateAnyOf(anyOf []any, data any, path string, errs *SchemaValidationErrors) {
	for _, schema := range anyOf {
		schemaMap, ok := schema.(map[string]any)
		if !ok {
			continue
		}
		testErrs := &SchemaValidationErrors{}
		validateType(schemaMap, data, path, testErrs)
		if !testErrs.HasErrors() {
			return
		}
	}
	errs.Add(path, "value does not match any schema in anyOf", data)
}

func joinPath(base, field string) string {
	if base == "" {
		return field
	}
	return base + "." + field
}
