// Package admission derives the schema fingerprint and registration token that
// identify one tool registry admission. The registry and generator both call
// this package so generated deployment tokens cannot drift from server routing.
package admission

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

type (
	// Schema contains every toolset field that changes registry routing identity.
	Schema struct {
		Name        string
		Description *string
		Version     *string
		Tags        []string
		Tools       []ToolSchema
	}

	// ToolSchema contains every tool field that changes registry routing identity.
	ToolSchema struct {
		Name                   string
		Description            *string
		Tags                   []string
		PayloadSchema          []byte
		ExecutionPayloadSchema []byte
		ResultSchema           []byte
		SidecarSchema          []byte
	}

	fingerprintTool struct {
		Name                   string   `json:"name"`
		Description            *string  `json:"description,omitempty"`
		Tags                   []string `json:"tags,omitempty"`
		PayloadSchema          string   `json:"payload_schema"`
		ExecutionPayloadSchema string   `json:"execution_payload_schema"`
		ResultSchema           string   `json:"result_schema"`
		SidecarSchema          string   `json:"sidecar_schema,omitempty"`
	}
)

// #nosec G101 -- this public protocol domain separator is not a credential.
const registrationTokenDomain = "goa-ai/tool-registry-admission/v2\x00"

// SchemaFingerprint returns the lowercase SHA-256 identity of the complete
// toolset schema after sorting fields whose order has no meaning.
func SchemaFingerprint(schema Schema) string {
	tools := make([]fingerprintTool, len(schema.Tools))
	for i, tool := range schema.Tools {
		tools[i] = fingerprintTool{
			Name:                   tool.Name,
			Description:            tool.Description,
			Tags:                   sortedStrings(tool.Tags),
			PayloadSchema:          string(tool.PayloadSchema),
			ExecutionPayloadSchema: string(tool.ExecutionPayloadSchema),
			ResultSchema:           string(tool.ResultSchema),
			SidecarSchema:          string(tool.SidecarSchema),
		}
	}
	sort.SliceStable(tools, func(i, j int) bool {
		return tools[i].Name < tools[j].Name
	})
	body, err := json.Marshal(struct {
		Name        string            `json:"name"`
		Description *string           `json:"description,omitempty"`
		Version     *string           `json:"version,omitempty"`
		Tags        []string          `json:"tags,omitempty"`
		Tools       []fingerprintTool `json:"tools"`
	}{
		Name:        schema.Name,
		Description: schema.Description,
		Version:     schema.Version,
		Tags:        sortedStrings(schema.Tags),
		Tools:       tools,
	})
	if err != nil {
		panic(fmt.Sprintf("marshal admission schema: %v", err))
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// RegistrationToken binds one schema fingerprint, deployment revision, and
// wire protocol version into the exact identity used for routing.
func RegistrationToken(schemaFingerprint, admissionRevision string, wireProtocolVersion int) (string, error) {
	schemaDigest, err := hex.DecodeString(schemaFingerprint)
	if err != nil {
		return "", fmt.Errorf("decode schema fingerprint: %w", err)
	}
	if len(schemaDigest) != sha256.Size {
		return "", fmt.Errorf("schema fingerprint must contain %d bytes", sha256.Size)
	}
	if wireProtocolVersion < 0 || uint64(wireProtocolVersion) > math.MaxUint32 {
		return "", fmt.Errorf("wire protocol version must fit uint32")
	}
	var protocolVersion [4]byte
	// #nosec G115 -- the range check above proves this conversion is exact.
	binary.BigEndian.PutUint32(protocolVersion[:], uint32(wireProtocolVersion))
	var revisionLength [4]byte
	// #nosec G115 -- the public runtime validates revisions at 256 bytes.
	binary.BigEndian.PutUint32(revisionLength[:], uint32(len(admissionRevision)))
	body := make(
		[]byte,
		0,
		len(registrationTokenDomain)+len(protocolVersion)+sha256.Size+len(revisionLength)+len(admissionRevision),
	)
	body = append(body, registrationTokenDomain...)
	body = append(body, protocolVersion[:]...)
	body = append(body, schemaDigest...)
	body = append(body, revisionLength[:]...)
	body = append(body, admissionRevision...)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

// sortedStrings returns a sorted copy for schema fields whose order has no
// effect on identity.
func sortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)
	return sorted
}
