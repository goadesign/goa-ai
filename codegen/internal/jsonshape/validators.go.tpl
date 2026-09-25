{{define "json-value-validators"}}{{- range .JSONValidators }}

// {{ .Name }} checks one value whose JSON shape is fixed by the generated Goa type.
func {{ .Name }}(path string, value any, description string) error {
    {{- if eq .Kind "any" }}
    return nil
    {{- else }}
    field := path
    if field == "" {
        field = "$payload"
    }
    if value == nil {
        return invalidGeneratedFieldTypeError(field, {{ printf "%q" .Expected }}, "null", description)
    }
    {{- $usesTyped := or .SignedInteger .UnsignedInteger (eq .Kind "object") (eq .Kind "array") (eq .Kind "map") (eq .Kind "union") }}
    {{- if or (eq .Expected "integer") (eq .Expected "number") }}
    {{ if $usesTyped }}typed{{ else }}_{{ end }}, ok := value.(json.Number)
    {{- else if eq .Expected "string" }}
    {{ if $usesTyped }}typed{{ else }}_{{ end }}, ok := value.(string)
    {{- else if eq .Expected "boolean" }}
    {{ if $usesTyped }}typed{{ else }}_{{ end }}, ok := value.(bool)
    {{- else if eq .Expected "array" }}
    {{ if $usesTyped }}typed{{ else }}_{{ end }}, ok := value.([]any)
    {{- else if eq .Expected "object" }}
    {{ if $usesTyped }}typed{{ else }}_{{ end }}, ok := value.(map[string]any)
    {{- end }}
    if !ok {
        return invalidGeneratedFieldTypeError(field, {{ printf "%q" .Expected }}, decodedJSONType(value), description)
    }
    {{- if .SignedInteger }}
    if _, err := strconv.ParseInt(typed.String(), 10, {{ if .IntegerBits }}{{ .IntegerBits }}{{ else }}strconv.IntSize{{ end }}); err != nil {
        return invalidGeneratedFieldTypeError(field, "integer", "number", description)
    }
    {{- else if .UnsignedInteger }}
    if _, err := strconv.ParseUint(typed.String(), 10, {{ if .IntegerBits }}{{ .IntegerBits }}{{ else }}strconv.IntSize{{ end }}); err != nil {
        return invalidGeneratedFieldTypeError(field, "integer", "number", description)
    }
    {{- end }}
    {{- if eq .Kind "union" }}
    for key := range typed {
        if key != {{printf "%q" .TypeKey}} && key != {{printf "%q" .ValueKey}} {
            return unknownJSONFieldError(path, key, []string{ {{printf "%q" .TypeKey}}, {{printf "%q" .ValueKey}} })
        }
    }
    discriminator, ok := typed[{{printf "%q" .TypeKey}}].(string)
    if !ok { return fmt.Errorf("%s: missing or invalid union discriminator", field) }
    branch, exists := typed[{{printf "%q" .ValueKey}}]
    if !exists || branch == nil { return fmt.Errorf("%s: missing union value", field) }
    switch discriminator {
    {{- $union := . }}
    {{- range .Branches }}
    case {{printf "%q" .Name}}:
        return {{.Call.Name}}(generatedJSONChildPath(path, {{printf "%q" $union.ValueKey}}, false), branch, {{printf "%q" .Call.Description}})
    {{- end }}
    default:
        return fmt.Errorf("%s: unknown union discriminator %q", field, discriminator)
    }
    {{- else if eq .Kind "object" }}
    keys := make([]string, 0, len(typed))
    for key := range typed {
        keys = append(keys, key)
    }
    sort.Strings(keys)
    for _, key := range keys {
        switch key {
        {{- range .Fields }}
        case {{ printf "%q" .Name }}:
            {{- if .Call }}
            if err := {{ .Call.Name }}(
                generatedJSONChildPath(path, key, false),
                typed[key],
                {{- if .Call.InheritDescription }}description{{ else }}{{ printf "%q" .Call.Description }}{{ end }},
            ); err != nil {
                return err
            }
            {{- end }}
        {{- end }}
        default:
            return unknownJSONFieldError(path, key, []string{
                {{- range .Fields }}
                {{ printf "%q" .Name }},
                {{- end }}
            })
        }
    }
    {{- else if eq .Kind "array" }}
    for index, item := range typed {
        {{- if .Element.AllowNull }}
        if item == nil {
            continue
        }
        {{- end }}
        if err := {{ .Element.Name }}(
            generatedJSONChildPath(path, strconv.Itoa(index), true),
            item,
            {{- if .Element.InheritDescription }}description{{ else }}{{ printf "%q" .Element.Description }}{{ end }},
        ); err != nil {
            return err
        }
    }
    {{- else if eq .Kind "map" }}
    keys := make([]string, 0, len(typed))
    for key := range typed {
        keys = append(keys, key)
    }
    sort.Strings(keys)
    for _, key := range keys {
        {{- if .Element.AllowNull }}
        if typed[key] == nil {
            continue
        }
        {{- end }}
        if err := {{ .Element.Name }}(
            generatedJSONChildPath(path, key, true),
            typed[key],
            {{- if .Element.InheritDescription }}description{{ else }}{{ printf "%q" .Element.Description }}{{ end }},
        ); err != nil {
            return err
        }
    }
    {{- end }}
    return nil
    {{- end }}
}
{{- end }}

{{end}}
