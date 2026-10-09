// Package mcp compares Skill YAML with the complete JSON discovery value.
// YAML nodes keep numeric spelling intact, so large integers are not rounded.
// Aliases share parsed nodes; mappings are resolved once instead of expanding
// repeated aliases. Cycles and values without a JSON representation are rejected.
package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type (
	// skillYAMLComparison retains resolved mappings for one fetched YAML block.
	// Cached node references avoid copying entire trees when aliases are reused.
	skillYAMLComparison struct {
		mappings map[*yaml.Node]map[string]*yaml.Node
	}
)

var skillDecimalSyntax = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

// compareSkillFrontmatter compares every field with already validated discovery
// JSON. Parsing keeps exact JSON numbers and does not replace the retained entry.
func compareSkillFrontmatter(frontmatter []byte, advertised json.RawMessage) error {
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(frontmatter))
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("mcp: invalid Skill YAML frontmatter: %w", err)
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("mcp: Skill frontmatter must contain one YAML document")
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return errors.New("mcp: Skill frontmatter must be a mapping")
	}
	if err := checkSkillYAMLCycles(&document, make(map[*yaml.Node]bool)); err != nil {
		return err
	}
	var expected any
	jsonDecoder := json.NewDecoder(bytes.NewReader(advertised))
	jsonDecoder.UseNumber()
	if err := jsonDecoder.Decode(&expected); err != nil {
		return fmt.Errorf("mcp: invalid retained Skill frontmatter: %w", err)
	}
	comparison := &skillYAMLComparison{mappings: make(map[*yaml.Node]map[string]*yaml.Node)}
	if err := comparison.compare(document.Content[0], expected); err != nil {
		return fmt.Errorf("mcp: Skill YAML frontmatter differs from the retained entry: %w", err)
	}
	return nil
}

// checkSkillYAMLCycles visits each parsed node once. An alias may reuse an
// earlier value but cannot point back into a value still being inspected.
func checkSkillYAMLCycles(node *yaml.Node, visited map[*yaml.Node]bool) error {
	if complete, exists := visited[node]; exists {
		if !complete {
			return errors.New("mcp: Skill YAML contains an alias cycle")
		}
		return nil
	}
	visited[node] = false
	for _, child := range node.Content {
		if err := checkSkillYAMLCycles(child, visited); err != nil {
			return err
		}
	}
	if node.Kind == yaml.AliasNode {
		if err := checkSkillYAMLCycles(node.Alias, visited); err != nil {
			return err
		}
	}
	visited[node] = true
	return nil
}

// equalSkillNumber compares decimal coefficients and exponents without
// allocating the exponent's expanded number. Integer bases are decoded exactly.
func equalSkillNumber(node *yaml.Node, expected json.Number) bool {
	actual := strings.ReplaceAll(node.Value, "_", "")
	if node.ShortTag() == "!!int" {
		integer, ok := new(big.Int).SetString(actual, 0)
		if !ok {
			return false
		}
		actual = integer.String()
	}
	actualCoefficient, actualExponent, ok := skillDecimal(actual)
	if !ok {
		return false
	}
	expectedCoefficient, expectedExponent, ok := skillDecimal(expected.String())
	return ok && actualCoefficient == expectedCoefficient && actualExponent.Cmp(expectedExponent) == 0
}

// skillDecimal reduces a finite number to its signed significant digits and
// decimal exponent. The result grows with source text, never exponent magnitude.
func skillDecimal(value string) (string, *big.Int, bool) {
	if !skillDecimalSyntax.MatchString(value) {
		return "", nil, false
	}
	coefficient, exponentText, hasExponent := strings.Cut(strings.ToLower(value), "e")
	exponent := new(big.Int)
	if hasExponent {
		if _, ok := exponent.SetString(exponentText, 10); !ok {
			return "", nil, false
		}
	}
	negative := strings.HasPrefix(coefficient, "-")
	coefficient = strings.TrimPrefix(strings.TrimPrefix(coefficient, "-"), "+")
	if point := strings.IndexByte(coefficient, '.'); point >= 0 {
		exponent.Sub(exponent, big.NewInt(int64(len(coefficient)-point-1)))
		coefficient = coefficient[:point] + coefficient[point+1:]
	}
	coefficient = strings.TrimLeft(coefficient, "0")
	if coefficient == "" {
		return "0", new(big.Int), true
	}
	significant := strings.TrimRight(coefficient, "0")
	exponent.Add(exponent, big.NewInt(int64(len(coefficient)-len(significant))))
	if negative {
		significant = "-" + significant
	}
	return significant, exponent, true
}

// compare walks the expected JSON tree and the corresponding parsed YAML nodes.
// Aliases retain their original values; no value is coerced to satisfy a mismatch.
func (c *skillYAMLComparison) compare(node *yaml.Node, expected any) error {
	if node.Kind == yaml.AliasNode {
		return c.compare(node.Alias, expected)
	}
	switch value := expected.(type) {
	case map[string]any:
		fields, err := c.mapping(node)
		if err != nil {
			return err
		}
		if len(fields) != len(value) {
			return errors.New("mapping fields differ")
		}
		for name, expectedField := range value {
			field, found := fields[name]
			if !found {
				return fmt.Errorf("field %q is absent", name)
			}
			if err := c.compare(field, expectedField); err != nil {
				return fmt.Errorf("field %q: %w", name, err)
			}
		}
		return nil
	case []any:
		if node.Kind != yaml.SequenceNode || node.ShortTag() != "!!seq" || len(node.Content) != len(value) {
			return errors.New("array shape differs")
		}
		for index, element := range value {
			if err := c.compare(node.Content[index], element); err != nil {
				return fmt.Errorf("array index %d: %w", index, err)
			}
		}
		return nil
	case string:
		if node.Kind == yaml.ScalarNode && node.ShortTag() == "!!str" && node.Value == value {
			return nil
		}
	case json.Number:
		if node.Kind == yaml.ScalarNode && (node.ShortTag() == "!!int" || node.ShortTag() == "!!float") && equalSkillNumber(node, value) {
			return nil
		}
	case bool:
		var actual bool
		if node.Kind == yaml.ScalarNode && node.ShortTag() == "!!bool" && node.Decode(&actual) == nil && actual == value {
			return nil
		}
	case nil:
		if node.Kind == yaml.ScalarNode && node.ShortTag() == "!!null" {
			return nil
		}
	}
	return errors.New("value or JSON type differs")
}

// mapping resolves explicit string fields and ordinary YAML merge sources.
// Explicit fields override merged fields; earlier merge sources win duplicates.
func (c *skillYAMLComparison) mapping(node *yaml.Node) (map[string]*yaml.Node, error) {
	if node.Kind == yaml.AliasNode {
		return c.mapping(node.Alias)
	}
	if node.Kind != yaml.MappingNode || node.ShortTag() != "!!map" {
		return nil, errors.New("expected a JSON-compatible YAML mapping")
	}
	if fields, found := c.mappings[node]; found {
		return fields, nil
	}
	fields := make(map[string]*yaml.Node)
	var merge *yaml.Node
	for index := 0; index < len(node.Content); index += 2 {
		key, value := node.Content[index], node.Content[index+1]
		for key.Kind == yaml.AliasNode {
			key = key.Alias
		}
		if key.Kind != yaml.ScalarNode {
			return nil, errors.New("YAML mapping keys must be strings")
		}
		if key.Value == "<<" && key.ShortTag() == "!!merge" {
			if merge != nil {
				return nil, errors.New("duplicate YAML merge key")
			}
			merge = value
			continue
		}
		if key.ShortTag() != "!!str" {
			return nil, errors.New("YAML mapping keys must be strings")
		}
		if _, duplicate := fields[key.Value]; duplicate {
			return nil, fmt.Errorf("duplicate YAML field %q", key.Value)
		}
		fields[key.Value] = value
	}
	if merge != nil {
		for merge.Kind == yaml.AliasNode {
			merge = merge.Alias
		}
		sources := []*yaml.Node{merge}
		if merge.Kind == yaml.SequenceNode {
			sources = merge.Content
		}
		for _, source := range sources {
			merged, err := c.mapping(source)
			if err != nil {
				return nil, err
			}
			for name, value := range merged {
				if _, explicit := fields[name]; !explicit {
					fields[name] = value
				}
			}
		}
	}
	c.mappings[node] = fields
	return fields, nil
}
