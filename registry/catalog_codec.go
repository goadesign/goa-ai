// Package registry reuses validated toolset JSON and compact discovery metadata.
// Full schemas are decoded only for validation or callers that need them.
// Admission state is decoded and checked on every Redis read.
package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
)

type (
	// catalogToolset retains serialized schemas and compact information, without
	// keeping the decoded consumer metadata graph alive between requests.
	catalogToolset struct {
		raw         json.RawMessage
		info        *genregistry.ToolsetInfo
		fingerprint string
	}

	// catalogToolsetJSON retains every occurrence so duplicate JSON members
	// cannot conceal an invalid definition before the final member.
	catalogToolsetJSON []json.RawMessage
)

// MarshalJSON retains the saved definition when only admission state changes.
//
//nolint:unparam // encoding/json requires the error result.
func (t *catalogToolset) MarshalJSON() ([]byte, error) {
	return t.raw, nil
}

// UnmarshalJSON keeps each member for the same ordered decoding that a Toolset
// pointer would receive from encoding/json.
//
//nolint:unparam // encoding/json requires the error result.
func (t *catalogToolsetJSON) UnmarshalJSON(raw []byte) error {
	*t = append(*t, bytes.Clone(raw))
	return nil
}

// newCatalogToolset serializes registration inputs and copies their compact
// information so later input changes cannot affect the saved definition.
func newCatalogToolset(toolset *genregistry.Toolset, fingerprint string) (*catalogToolset, error) {
	raw, err := json.Marshal(toolset)
	if err != nil {
		return nil, fmt.Errorf("marshal toolset %q: %w", toolset.Name, err)
	}
	return &catalogToolset{raw: raw, info: toolsetToInfo(toolset), fingerprint: fingerprint}, nil
}

// toolsetToInfo retains only the fields needed for discovery and health.
func toolsetToInfo(toolset *genregistry.Toolset) *genregistry.ToolsetInfo {
	return copyToolsetInfo(&genregistry.ToolsetInfo{
		Name:         toolset.Name,
		Description:  toolset.Description,
		Version:      toolset.Version,
		Tags:         toolset.Tags,
		ToolCount:    len(toolset.Tools),
		RegisteredAt: toolset.RegisteredAt,
	})
}

// copyToolsetInfo gives the receiver ownership of every mutable summary field.
func copyToolsetInfo(info *genregistry.ToolsetInfo) *genregistry.ToolsetInfo {
	owned := *info
	if info.Description != nil {
		value := *info.Description
		owned.Description = &value
	}
	if info.Version != nil {
		value := *info.Version
		owned.Version = &value
	}
	owned.Tags = slices.Clone(info.Tags)
	return &owned
}

// decodeCatalogToolset applies the same strict persisted-data checks on a new
// definition and on independent results returned to callers.
func decodeCatalogToolset(raw []byte, toolset **genregistry.Toolset) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(toolset); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing JSON value")
	}
	return nil
}

// decode gives a caller its own complete definition for schema-dependent work.
func (t *catalogToolset) decode() (*genregistry.Toolset, error) {
	var result *genregistry.Toolset
	if err := decodeCatalogToolset(t.raw, &result); err != nil {
		return nil, fmt.Errorf("decode toolset %q: %w", t.info.Name, err)
	}
	return result, nil
}

// marshalCatalogEntry encodes the one persisted admission record.
func marshalCatalogEntry(entry catalogEntry) (string, error) {
	body, err := json.Marshal(entry)
	if err != nil {
		return "", fmt.Errorf("marshal toolset %q admission: %w", entry.Toolset.info.Name, err)
	}
	return string(body), nil
}

// parseCatalogEntry validates persisted admission identity and lease state.
func (c *toolsetCatalog) parseCatalogEntry(name, body string) (catalogEntry, error) {
	var entry catalogEntry
	envelope := struct {
		*catalogEntry
		Toolset catalogToolsetJSON `json:"toolset"`
	}{catalogEntry: &entry}
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return catalogEntry{}, fmt.Errorf("unmarshal toolset %q: %w", name, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return catalogEntry{}, fmt.Errorf("unmarshal toolset %q: trailing JSON value", name)
	}
	c.definitionsMu.Lock()
	defer c.definitionsMu.Unlock()
	definition := c.definitions[name]
	if definition == nil || len(envelope.Toolset) != 1 || !bytes.Equal(definition.raw, envelope.Toolset[0]) {
		var toolset *genregistry.Toolset
		for _, raw := range envelope.Toolset {
			if err := decodeCatalogToolset(raw, &toolset); err != nil {
				return catalogEntry{}, fmt.Errorf("unmarshal toolset %q: %w", name, err)
			}
		}
		if toolset == nil || toolset.Name != name {
			return catalogEntry{}, fmt.Errorf("toolset %q has invalid toolset payload", name)
		}
		fingerprint, err := toolsetSchemaFingerprint(toolset)
		if err != nil {
			return catalogEntry{}, err
		}
		raw := envelope.Toolset[0]
		if len(envelope.Toolset) > 1 {
			// Preserve encoding/json's merge behavior for repeated objects, then
			// save their complete definition as one toolset member.
			raw, err = json.Marshal(toolset)
			if err != nil {
				return catalogEntry{}, fmt.Errorf("marshal toolset %q: %w", name, err)
			}
		}
		definition = &catalogToolset{raw: raw, info: toolsetToInfo(toolset), fingerprint: fingerprint}
	}
	entry.Toolset = definition
	if entry.RegisteredAt == "" || entry.Toolset.info.RegisteredAt != entry.RegisteredAt {
		return catalogEntry{}, fmt.Errorf("toolset %q has invalid registered_at", name)
	}
	if _, err := time.Parse(time.RFC3339Nano, entry.RegisteredAt); err != nil {
		return catalogEntry{}, fmt.Errorf("toolset %q invalid registered_at: %w", name, err)
	}
	if err := toolregistry.ValidateAdmissionRevision(entry.AdmissionRevision); err != nil {
		return catalogEntry{}, fmt.Errorf("toolset %q invalid admission revision: %w", name, err)
	}
	if err := toolregistry.ValidateWireProtocolVersion(entry.WireProtocolVersion); err != nil {
		return catalogEntry{}, fmt.Errorf("toolset %q invalid wire protocol version: %w", name, err)
	}
	fingerprint := definition.fingerprint
	if entry.SchemaFingerprint != fingerprint {
		return catalogEntry{}, fmt.Errorf("toolset %q schema fingerprint does not match canonical schema", name)
	}
	token, err := admissionRegistrationToken(
		fingerprint,
		entry.AdmissionRevision,
		entry.WireProtocolVersion,
	)
	if err != nil {
		return catalogEntry{}, fmt.Errorf("derive toolset %q admission token: %w", name, err)
	}
	if entry.RegistrationToken != token {
		return catalogEntry{}, fmt.Errorf("toolset %q registration token does not match admission identity", name)
	}
	switch entry.State {
	case catalogEntryActive, catalogEntryRetired:
	default:
		return catalogEntry{}, fmt.Errorf("toolset %q has invalid catalog state %q", name, entry.State)
	}
	if entry.ProviderLeases == nil {
		return catalogEntry{}, fmt.Errorf("toolset %q missing provider lease map", name)
	}
	if entry.HealthEpoch == 0 {
		return catalogEntry{}, fmt.Errorf("toolset %q has invalid health epoch", name)
	}
	if entry.LastPongUnixNano < 0 {
		return catalogEntry{}, fmt.Errorf("toolset %q has invalid last pong timestamp", name)
	}
	for leaseKey, lease := range entry.ProviderLeases {
		if _, _, err := parseProviderLeaseKey(leaseKey); err != nil {
			return catalogEntry{}, fmt.Errorf("toolset %q has invalid provider lease key: %w", name, err)
		}
		if lease.ExpiresAtUnixMilli <= 0 {
			return catalogEntry{}, fmt.Errorf("toolset %q has invalid provider lease deadline", name)
		}
	}
	if entry.RetiredTokens == nil {
		return catalogEntry{}, fmt.Errorf("toolset %q missing retired registration token set", name)
	}
	for retiredToken := range entry.RetiredTokens {
		if err := toolregistry.ValidateRegistrationToken(retiredToken); err != nil {
			return catalogEntry{}, fmt.Errorf("toolset %q invalid retired registration token: %w", name, err)
		}
	}
	if entry.State == catalogEntryActive {
		if _, retired := entry.RetiredTokens[entry.RegistrationToken]; retired {
			return catalogEntry{}, fmt.Errorf("toolset %q active token is retired", name)
		}
	} else {
		if _, retired := entry.RetiredTokens[entry.RegistrationToken]; !retired {
			return catalogEntry{}, fmt.Errorf("toolset %q retired token is missing its tombstone", name)
		}
	}
	c.definitions[name] = definition
	return entry, nil
}
