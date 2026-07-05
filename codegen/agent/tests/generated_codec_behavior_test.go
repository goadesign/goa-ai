package tests

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	"goa.design/goa-ai/codegen/testhelpers"
	gcodegen "goa.design/goa/v3/codegen"
)

func TestGeneratedCodecInvalidFieldTypeBehavior(t *testing.T) {
	root := writeGeneratedModule(t, testhelpers.BuildAndGenerateWithPkg(t, "generated.local", testscenarios.ArgsInlineObject()))
	writeGeneratedPackageTest(t, root, "alpha/toolsets/math/http/validate_stub.go", `package http

func ValidateAddPayloadTransport(v *AddPayloadTransport) error {
	return nil
}

func ValidateAddResultTransport(v *AddResultTransport) error {
	return nil
}
`)
	writeGeneratedPackageTest(t, root, "alpha/toolsets/math/codecs_behavior_test.go", `package math

import (
	"errors"
	"testing"

	"goa.design/goa-ai/runtime/agent/tools"
)

func TestUnmarshalAddPayloadInvalidFieldType(t *testing.T) {
	_, err := UnmarshalAddPayload([]byte(`+"`"+`{"left":"one","right":2}`+"`"+`))
	if err == nil {
		t.Fatal("expected error")
	}
	var validation *tools.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	issues := validation.Issues()
	if len(issues) != 1 {
		t.Fatalf("expected one issue, got %d", len(issues))
	}
	issue := issues[0]
	if issue.Field != "left" || issue.Constraint != "invalid_field_type" || issue.ExpectedJSONType != "integer" || issue.ActualJSONType != "string" {
		t.Fatalf("unexpected issue: %#v", issue)
	}
}
`)

	runGeneratedMathGoTest(t, root)
}

func TestGeneratedCodecUnknownFieldBehavior(t *testing.T) {
	root := writeGeneratedModule(t, testhelpers.BuildAndGenerateWithPkg(t, "generated.local", testscenarios.DeepNestedValidations()))
	writeGeneratedPackageTest(t, root, "alpha/toolsets/deep/http/validate_stub.go", `package http

func ValidateValidatePayloadTransport(v *ValidatePayloadTransport) error {
	return nil
}

func ValidateValidateResultTransport(v *ValidateResultTransport) error {
	return nil
}
`)
	writeGeneratedPackageTest(t, root, "alpha/toolsets/deep/codecs_behavior_test.go", `package deep

import (
	"errors"
	"testing"

	"goa.design/goa-ai/runtime/agent/tools"
)

func TestUnmarshalValidatePayloadRejectsUnknownRootField(t *testing.T) {
	_, err := UnmarshalValidatePayload([]byte(`+"`"+`{"root":"r","child":{"mid":"m","child":{"leaf":"l"}},"scope_context":"compressor_2"}`+"`"+`))
	if err == nil {
		t.Fatal("expected error")
	}
	assertUnknownFieldIssue(t, err, "scope_context", []string{"child", "labels", "root"})
}

func TestUnmarshalValidatePayloadRejectsUnknownNestedField(t *testing.T) {
	_, err := UnmarshalValidatePayload([]byte(`+"`"+`{"root":"r","child":{"mid":"m","child":{"leaf":"l","extra":true}}}`+"`"+`))
	if err == nil {
		t.Fatal("expected error")
	}
	assertUnknownFieldIssue(t, err, "child.child.extra", []string{"leaf"})
}

func TestUnmarshalValidatePayloadPreservesOpenMapKeys(t *testing.T) {
	payload, err := UnmarshalValidatePayload([]byte(`+"`"+`{"root":"r","child":{"mid":"m","child":{"leaf":"l"}},"labels":{"scope_context":"compressor_2","custom":"value"}}`+"`"+`))
	if err != nil {
		t.Fatal(err)
	}
	if payload.Labels["scope_context"] != "compressor_2" || payload.Labels["custom"] != "value" {
		t.Fatalf("unexpected labels: %#v", payload.Labels)
	}
}

func assertUnknownFieldIssue(t *testing.T, err error, field string, allowed []string) {
	t.Helper()
	var validation *tools.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	issues := validation.Issues()
	if len(issues) != 1 {
		t.Fatalf("expected one issue, got %d", len(issues))
	}
	issue := issues[0]
	if issue.Field != field || issue.Constraint != "unknown_field" {
		t.Fatalf("unexpected issue: %#v", issue)
	}
	if !sameStrings(issue.Allowed, allowed) {
		t.Fatalf("unexpected allowed keys: got %#v want %#v", issue.Allowed, allowed)
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
`)

	runGeneratedDeepGoTest(t, root)
}

func TestGeneratedCodecBoundedResultProjectionBehavior(t *testing.T) {
	root := writeGeneratedModuleWithPath(t, "generated.local/gen", testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", testscenarios.ServiceToolsetBindSelfBoundedResult()))
	removeGeneratedPackageFile(t, root, "alpha/toolsets/lookup/provider.go")
	removeGeneratedPackageFile(t, root, "alpha/toolsets/lookup/transforms.go")
	writeGeneratedPackageTest(t, root, "alpha/toolsets/lookup/http/validate_stub.go", `package http

func ValidateSearchPayloadTransport(v *SearchPayloadTransport) error {
	return nil
}

func ValidateSearchResultTransport(v *SearchResultTransport) error {
	return nil
}
`)
	writeGeneratedPackageTest(t, root, "alpha/toolsets/lookup/codecs_behavior_test.go", `package lookup

import (
	"errors"
	"testing"

	"goa.design/goa-ai/runtime/agent/tools"
)

func TestUnmarshalSearchResultAcceptsBoundedProjectionFields(t *testing.T) {
	result, err := UnmarshalSearchResult([]byte(`+"`"+`{"results":["compressor_2"],"returned":1,"truncated":false,"total":3,"next_cursor":"cursor_2","refinement_hint":"narrow the device kind"}`+"`"+`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 || result.Results[0] != "compressor_2" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestUnmarshalSearchResultRejectsUnknownResultField(t *testing.T) {
	_, err := UnmarshalSearchResult([]byte(`+"`"+`{"results":["compressor_2"],"unexpected":true}`+"`"+`))
	if err == nil {
		t.Fatal("expected error")
	}
	var validation *tools.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	issues := validation.Issues()
	if len(issues) != 1 {
		t.Fatalf("expected one issue, got %d", len(issues))
	}
	issue := issues[0]
	if issue.Field != "unexpected" || issue.Constraint != "unknown_field" {
		t.Fatalf("unexpected issue: %#v", issue)
	}
	if !sameStrings(issue.Allowed, []string{"next_cursor", "refinement_hint", "results", "returned", "total", "truncated"}) {
		t.Fatalf("unexpected allowed keys: %#v", issue.Allowed)
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
`)

	runGeneratedLookupGoTest(t, root)
}

func TestGeneratedCodecUnionInvalidFieldTypeBehavior(t *testing.T) {
	root := writeGeneratedModule(t, testhelpers.BuildAndGenerateWithPkg(t, "generated.local", testscenarios.ArgsUnionSumTypes()))
	writeGeneratedPackageTest(t, root, "alpha/toolsets/union/http/validate_stub.go", `package http

func ValidateEchoPayloadTransport(v *EchoPayloadTransport) error {
	return nil
}

func ValidateEchoResultTransport(v *EchoResultTransport) error {
	return nil
}
`)
	writeGeneratedPackageTest(t, root, "alpha/toolsets/union/codecs_behavior_test.go", `package union

import (
	"errors"
	"strings"
	"testing"

	"goa.design/goa-ai/runtime/agent/tools"
)

func TestUnmarshalEchoPayloadInvalidUnionEnvelopeType(t *testing.T) {
	_, err := UnmarshalEchoPayload([]byte(`+"`"+`{"id":"req_1","value":"bad"}`+"`"+`))
	if err == nil {
		t.Fatal("expected error")
	}
	var validation *tools.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	issues := validation.Issues()
	if len(issues) != 1 {
		t.Fatalf("expected one issue, got %d", len(issues))
	}
	issue := issues[0]
	if issue.Field != "value" || issue.Constraint != "invalid_field_type" || issue.ExpectedJSONType != "object" || issue.ActualJSONType != "string" {
		t.Fatalf("unexpected issue: %#v", issue)
	}
}

func TestUnmarshalEchoPayloadUsesSelectedUnionBranchType(t *testing.T) {
	_, err := UnmarshalEchoPayload([]byte(`+"`"+`{"id":"req_1","value":{"type":"number","value":"bad"}}`+"`"+`))
	if err == nil {
		t.Fatal("expected error")
	}
	var validation *tools.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	issues := validation.Issues()
	if len(issues) != 1 {
		t.Fatalf("expected one issue, got %d", len(issues))
	}
	issue := issues[0]
	if issue.Field != "value" || issue.Constraint != "invalid_field_type" || issue.ExpectedJSONType != "integer" || issue.ActualJSONType != "string" {
		t.Fatalf("unexpected issue: %#v", issue)
	}
}

func TestUnmarshalEchoPayloadRejectsUnknownUnionBranchFields(t *testing.T) {
	_, err := UnmarshalEchoPayload([]byte(`+"`"+`{"id":"req_1","value":{"type":"structured","value":{"label":"ready","extra":true}}}`+"`"+`))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unknown field") || !strings.Contains(err.Error(), "extra") {
		t.Fatalf("expected unknown field error for extra, got %v", err)
	}
}
`)

	runGeneratedUnionGoTest(t, root)
}

func TestGeneratedCodecModelJSONNamesBehavior(t *testing.T) {
	root := writeGeneratedModule(t, testhelpers.BuildAndGenerateWithPkg(t, "generated.local", testscenarios.ModelJSONNames()))
	writeGeneratedPackageTest(t, root, "alpha/toolsets/inspect/http/validate_stub.go", `package http

func ValidateInspectDevicePayloadTransport(v *InspectDevicePayloadTransport) error {
	return nil
}

func ValidateInspectDeviceResultTransport(v *InspectDeviceResultTransport) error {
	return nil
}
`)
	writeGeneratedPackageTest(t, root, "alpha/toolsets/inspect/codecs_behavior_test.go", `package inspect

import (
	"strings"
	"testing"
)

func TestUnmarshalInspectDevicePayloadAcceptsSnakeCase(t *testing.T) {
	payload, err := UnmarshalInspectDevicePayload([]byte(`+"`"+`{"device_alias":"ahu_1","render_ui":true,"source_ids":["temp"],"time_context":{"start_time":"2026-01-01T00:00:00Z","end_time":"2026-01-01T01:00:00Z"}}`+"`"+`))
	if err != nil {
		t.Fatal(err)
	}
	if payload.DeviceAlias != "ahu_1" || !payload.RenderUI || len(payload.SourceIds) != 1 || payload.SourceIds[0] != "temp" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	if payload.TimeContext.StartTime != "2026-01-01T00:00:00Z" || payload.TimeContext.EndTime != "2026-01-01T01:00:00Z" {
		t.Fatalf("unexpected time context: %#v", payload.TimeContext)
	}
}

func TestUnmarshalInspectDevicePayloadRejectsLowerCamel(t *testing.T) {
	_, err := UnmarshalInspectDevicePayload([]byte(`+"`"+`{"deviceAlias":"ahu_1","renderUi":true,"timeContext":{"startTime":"2026-01-01T00:00:00Z","endTime":"2026-01-01T01:00:00Z"}}`+"`"+`))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unknown field") || !strings.Contains(err.Error(), "deviceAlias") {
		t.Fatalf("expected unknown field error for deviceAlias, got %v", err)
	}
}

func TestMarshalInspectDevicePayloadEmitsSnakeCase(t *testing.T) {
	payload := &InspectDevicePayload{
		DeviceAlias: "ahu_1",
		RenderUI:    true,
		SourceIds:   []string{"temp"},
		TimeContext: &TimeContext{
			StartTime: "2026-01-01T00:00:00Z",
			EndTime:   "2026-01-01T01:00:00Z",
		},
	}
	data, err := MarshalInspectDevicePayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{"device_alias", "render_ui", "source_ids", "time_context", "start_time", "end_time"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in %s", want, got)
		}
	}
	for _, forbidden := range []string{"deviceAlias", "renderUi", "sourceIds", "timeContext", "startTime", "endTime"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("did not expect %q in %s", forbidden, got)
		}
	}
}
`)

	runGeneratedInspectGoTest(t, root)
}

func writeGeneratedModule(t *testing.T, files []*gcodegen.File) string {
	return writeGeneratedModuleWithPath(t, "generated.local", files)
}

func writeGeneratedModuleWithPath(t *testing.T, modulePath string, files []*gcodegen.File) string {
	t.Helper()
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../../..")
	require.NoError(t, err)
	goMod := "module " + modulePath + "\n\ngo 1.24\n\nrequire goa.design/goa-ai v0.0.0\n\nreplace goa.design/goa-ai => " + filepath.ToSlash(repoRoot) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte(goMod), 0o600))
	for _, file := range files {
		rel := strings.TrimPrefix(filepath.ToSlash(file.Path), "gen/")
		if strings.HasSuffix(rel, "/http/validate.go") {
			continue
		}
		path := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(fileContent(t, files, file.Path)), 0o600))
	}
	return root
}

func writeGeneratedPackageTest(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func removeGeneratedPackageFile(t *testing.T, root, rel string) {
	t.Helper()
	require.NoError(t, os.Remove(filepath.Join(root, filepath.FromSlash(rel))))
}

func runGeneratedMathGoTest(t *testing.T, root string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "test", "-mod=mod", "./alpha/toolsets/math"))
}

func runGeneratedDeepGoTest(t *testing.T, root string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "test", "-mod=mod", "./alpha/toolsets/deep"))
}

func runGeneratedLookupGoTest(t *testing.T, root string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "test", "-mod=mod", "./alpha/toolsets/lookup"))
}

func runGeneratedUnionGoTest(t *testing.T, root string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "test", "-mod=mod", "./alpha/toolsets/union"))
}

func runGeneratedInspectGoTest(t *testing.T, root string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "test", "-mod=mod", "./alpha/toolsets/inspect"))
}

func runGeneratedGoTestCommand(t *testing.T, root string, cmd *exec.Cmd) {
	t.Helper()
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s failed: %v\n%s", strings.Join(cmd.Args, " "), err, out.String())
	}
}
