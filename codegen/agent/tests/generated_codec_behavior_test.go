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
`)

	runGeneratedUnionGoTest(t, root)
}

func writeGeneratedModule(t *testing.T, files []*gcodegen.File) string {
	t.Helper()
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../../..")
	require.NoError(t, err)
	goMod := "module generated.local\n\ngo 1.24\n\nrequire goa.design/goa-ai v0.0.0\n\nreplace goa.design/goa-ai => " + filepath.ToSlash(repoRoot) + "\n"
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

func runGeneratedMathGoTest(t *testing.T, root string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "test", "-mod=mod", "./alpha/toolsets/math"))
}

func runGeneratedUnionGoTest(t *testing.T, root string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "test", "-mod=mod", "./alpha/toolsets/union"))
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
