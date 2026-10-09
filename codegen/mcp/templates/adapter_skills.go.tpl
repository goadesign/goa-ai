{{- if and .SkillCatalog .SkillLookup }}
// SkillsList invokes the configured catalog endpoint and returns complete entries.
// Discovery supplies metadata only; the host reads files when they are needed.
func (a *MCPAdapter) SkillsList(ctx context.Context, p {{ index .PayloadRefs "skills/list" }}) (*SkillsListResult, error) {
    ctx, span := otel.Tracer("goa-ai/mcp").Start(ctx, "mcp.skills/list")
    defer span.End()
    {{- with .SkillCatalog }}
    {{ template "discovery-call" . }}
    {{ .EntriesConversion }}
    seen := make(map[string]struct{}, len(skills))
    for index, entry := range skills {
        encoded, err := {{ .EntryEncoder }}({{ .Endpoint.ResultValue }}.{{ .EntriesField }}[index])
        if err == nil {
            err = mcpruntime.ValidateSkillEntry(ctx, encoded)
        }
        if err != nil {
            span.RecordError(err)
            span.SetStatus(codes.Error, err.Error())
            return nil, goa.PermanentError("internal_error", "%s", err.Error())
        }
        if _, duplicate := seen[entry.URI]; duplicate {
            failure := goa.PermanentError("internal_error", "skill catalog returned duplicate URI %q", entry.URI)
            span.RecordError(failure)
            span.SetStatus(codes.Error, failure.Error())
            return nil, failure
        }
        seen[entry.URI] = struct{}{}
    }
    return &SkillsListResult{ResultType: "complete", Meta: resultMeta(), TTLMs: 0, CacheScope: "private", Skills: skills, NextCursor: nextCursor}, nil
    {{- end }}
}

// SkillsGet invokes the configured lookup endpoint for the exact requested URI.
// A valid entry can be returned even when it was absent from skills/list.
func (a *MCPAdapter) SkillsGet(ctx context.Context, p {{ index .PayloadRefs "skills/get" }}) (*SkillsGetResult, error) {
    ctx, span := otel.Tracer("goa-ai/mcp").Start(ctx, "mcp.skills/get")
    defer span.End()
    {{- with .SkillLookup }}
    {{ template "discovery-call" . }}
    {{ .EntriesConversion }}
    encoded, err := {{ .EntryEncoder }}({{ .Endpoint.ResultValue }}.{{ .EntriesField }})
    if err == nil {
        err = mcpruntime.ValidateSkillEntry(ctx, encoded)
    }
    if err != nil {
        span.RecordError(err)
        span.SetStatus(codes.Error, err.Error())
        return nil, goa.PermanentError("internal_error", "%s", err.Error())
    }
    if skill.URI != p.URI {
        failure := goa.PermanentError("internal_error", "skill lookup returned a different URI")
        span.RecordError(failure)
        span.SetStatus(codes.Error, failure.Error())
        return nil, failure
    }
    return &SkillsGetResult{ResultType: "complete", Meta: resultMeta(), TTLMs: 0, CacheScope: "private", Skill: skill}, nil
    {{- end }}
}
{{- with .SkillCatalog }}{{ template "catalog-helpers" . }}{{ end }}
{{- with .SkillLookup }}{{ template "catalog-helpers" . }}{{ end }}
{{- end }}
