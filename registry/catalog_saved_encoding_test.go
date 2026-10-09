// Saved definitions must reload with their accepted identities and exact bytes.
// These synthetic declarations cover earlier and current generated encoding,
// service leases, native execution targets, and permanent service retirement.
package registry

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genrecords "goa.design/goa-ai/internal/testpresentation/gen/records/toolsets/records"
	internaladmission "goa.design/goa-ai/internal/toolregistry/admission"
	genregistryclient "goa.design/goa-ai/registry/gen/grpc/registry/client"
	genregistryserver "goa.design/goa-ai/registry/gen/grpc/registry/server"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
	toolcontract "goa.design/goa-ai/runtime/toolregistry/contract"
)

func TestCatalogSavedEncodingPreservesRegistration(t *testing.T) {
	for _, encoding := range []string{"earlier", "current"} {
		for _, native := range []bool{false, true} {
			for _, retired := range []bool{false, true} {
				name := encoding + "/service/active"
				if native {
					name = encoding + "/native/active"
				}
				if retired {
					name += "/retired"
				}
				t.Run(name, func(t *testing.T) {
					ctx := t.Context()
					clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
					store := newTestCatalogMap(clock)
					writer := newToolsetCatalog(store, clock)
					toolset := testDefinitionToolset()
					if native {
						declaration := testAgentToolset("revision/1")
						toolset = &genregistry.Toolset{Name: declaration.Name, Tools: declaration.Tools}
					}
					definition := testCatalogDefinition(t, toolset)
					definition.identity = &CatalogIdentity{Scope: "fixtures", Name: "records"}
					raw := definition.raw
					contract, err := json.Marshal(toolset.Tools[0].ConsumerContract)
					require.NoError(t, err)
					if encoding == "earlier" {
						previous := strings.Replace(string(contract), `,"RequiresUI":false,"TextOnly":null`, "", 1)
						require.NotEqual(t, string(contract), previous)
						raw = strings.Replace(raw, string(contract), previous, 1)
						contract = []byte(previous)
					}
					// The expected identity uses the explicitly saved contract as
					// admission input, independently of the reload decoder.
					fingerprint := savedFixtureFingerprint(toolset, contract)
					definition.raw = raw
					definition.fingerprint = fingerprint
					var state catalogState
					if native {
						state, err = writer.RegisterAgent(ctx, definition, "")
					} else {
						state, err = writer.Register(ctx, definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
					}
					require.NoError(t, err)
					if !native {
						require.NoError(t, writer.DrainProvider(ctx, toolset.Name, "provider", testIncarnationA, state.RegistrationToken, time.Minute))
					}
					if retired {
						require.NoError(t, writer.Retire(ctx, toolset.Name, state.RegistrationToken))
					}
					key := toolsetCatalogKey(toolset.Name)
					beforeState, beforeDefinition, membership, exists, err := store.Snapshot(ctx, key)
					require.NoError(t, err)
					require.True(t, exists)
					assert.Equal(t, raw, beforeDefinition)
					assert.Equal(t, retired && !native, membership)
					writes := store.definitionWrites
					cold := newToolsetCatalog(store, clock)
					require.NoError(t, cold.validatePersistedEntries(ctx))
					loaded, err := cold.snapshot(ctx, toolset.Name)
					require.NoError(t, err)
					assert.Equal(t, fingerprint, loaded.SchemaFingerprint)
					assert.Equal(t, state.RegistrationToken, loaded.RegistrationToken)
					assert.Equal(t, definition.identity, loaded.Identity)
					assert.Equal(t, raw, loaded.Toolset.raw)
					expectedState, err := parseCatalogState(toolset.Name, beforeState)
					require.NoError(t, err)
					assert.Equal(t, expectedState, loaded.catalogState)
					decoded, err := loaded.Toolset.decode("")
					require.NoError(t, err)
					assert.Equal(t, toolset, decoded)
					if native {
						assert.Equal(t, nativeAgentToken(fingerprint), loaded.RegistrationToken)
						spec, err := toolcontract.Compile(decoded.Tools[0])
						require.NoError(t, err)
						assert.True(t, spec.IsAgentTool)
						assert.Equal(t, "generic.agent", decoded.Tools[0].ConsumerContract.Agent.Executor)
						assert.Equal(t, "revision/1", decoded.Tools[0].ConsumerContract.Agent.Configuration)
					} else {
						assert.True(t, loaded.ProviderLeases[providerLeaseKey("provider", testIncarnationA)].Draining)
						assert.Equal(t, state.AdmissionRevision, loaded.AdmissionRevision)
						assert.Equal(t, state.WireProtocolVersion, loaded.WireProtocolVersion)
					}
					if retired {
						_, err = cold.ActiveRegistration(ctx, toolset.Name)
						require.ErrorIs(t, err, errToolsetNotFound)
						if !native {
							_, err = cold.Register(ctx, definition, testAdmissionRevisionA, "provider", testIncarnationB, time.Minute)
							require.ErrorIs(t, err, errAdmissionRetired)
						}
					} else {
						resolved, err := (&Service{catalog: cold}).ResolveToolset(ctx, &genregistry.GetToolsetPayload{Name: toolset.Name})
						require.NoError(t, err)
						assert.Equal(t, state.RegistrationToken, resolved.RegistrationToken)
						assert.Equal(t, toolset.Tools, resolved.Toolset.Tools)
					}
					afterState, afterDefinition, afterMembership, _, err := store.Snapshot(ctx, key)
					require.NoError(t, err)
					assert.Equal(t, beforeState, afterState)
					assert.Equal(t, beforeDefinition, afterDefinition)
					assert.Equal(t, membership, afterMembership)
					assert.Equal(t, writes, store.definitionWrites)
				})
			}
		}
	}
}

func TestCatalogSavedEncodingRejectsChangedContractBytes(t *testing.T) {
	catalog, store, _ := testDefinitionCatalog(t)
	key := toolsetCatalogKey("tools")
	state, raw, membership, _, err := store.Snapshot(t.Context(), key)
	require.NoError(t, err)
	for _, changed := range []string{
		strings.Replace(raw, `"Title":"original"`, `"Title":"changed"`, 1),
		strings.Replace(raw, `"Title":"original"`, `"Title" : "original"`, 1),
		strings.Replace(raw, `"Title":"original"`, `"Title":"\u006friginal"`, 1),
		strings.Replace(raw, `"RequiresUI":false`, `"RequiresUI":true`, 1),
		strings.Replace(raw, `,"RequiresUI":false,"TextOnly":null`, "", 1),
	} {
		require.NotEqual(t, raw, changed)
		_, err := catalog.decodeSnapshot(t.Context(), "tools", state, changed, membership)
		assert.ErrorContains(t, err, "schema fingerprint")
	}
}

func TestCatalogGeneratedDeclarationsKeepCurrentIdentity(t *testing.T) {
	toolset := genrecords.Toolset()
	fingerprint, err := toolcontract.Fingerprint(toolset)
	require.NoError(t, err)
	generated, err := genrecords.SchemaFingerprint(toolset.Name)
	require.NoError(t, err)
	assert.Equal(t, generated, fingerprint)
	request := genregistryclient.NewProtoRegisterRequest(&genregistry.RegisterPayload{
		Name: toolset.Name, Description: toolset.Description, Version: toolset.Version,
		Tags: toolset.Tags, Tools: toolset.Tools, ProviderID: "provider",
		AdmissionRevision: testAdmissionRevisionA, ProviderIncarnationID: testIncarnationA,
		WireProtocolVersion: toolregistry.WireProtocolVersion, SchemaFingerprint: generated,
	})
	require.NoError(t, genregistryserver.ValidateRegisterRequest(request))
	payload := genregistryserver.NewRegisterPayload(request)
	toolset = &genregistry.Toolset{
		Name: payload.Name, Description: payload.Description, Version: payload.Version,
		Tags: payload.Tags, Tools: payload.Tools,
	}
	transportFingerprint, err := toolcontract.Fingerprint(toolset)
	require.NoError(t, err)
	assert.Equal(t, payload.SchemaFingerprint, transportFingerprint)
	definition, err := newCatalogToolset(toolset, fingerprint, newSchemaValidator())
	require.NoError(t, err)
	decoded, savedFingerprint, err := internaladmission.SavedToolsetFingerprint([]byte(definition.raw))
	require.NoError(t, err)
	assert.Equal(t, fingerprint, savedFingerprint)
	assert.Equal(t, toolset, decoded)
	clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
	store := newTestCatalogMap(clock)
	writer := newToolsetCatalog(store, clock)
	state, err := writer.Register(t.Context(), definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
	require.NoError(t, err)
	cold := newToolsetCatalog(store, clock)
	require.NoError(t, cold.validatePersistedEntries(t.Context()))
	loaded, err := cold.ActiveRegistration(t.Context(), toolset.Name)
	require.NoError(t, err)
	assert.Equal(t, fingerprint, loaded.SchemaFingerprint)
	assert.Equal(t, state.RegistrationToken, loaded.RegistrationToken)
	assert.Equal(t, definition.raw, loaded.Toolset.raw)
}

func TestCatalogSavedMissingAndNullContractsPreserveIdentity(t *testing.T) {
	toolset := testDefinitionToolset()
	toolset.Tools[0].ConsumerContract = nil
	definition := testCatalogDefinition(t, toolset)
	for _, raw := range []string{
		definition.raw,
		strings.Replace(definition.raw, `,"ConsumerContract":null`, "", 1),
		" \n" + definition.raw + "\n ",
	} {
		clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
		store := newTestCatalogMap(clock)
		writer := newToolsetCatalog(store, clock)
		incoming := *definition
		incoming.raw = raw
		state, err := writer.Register(t.Context(), &incoming, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
		require.NoError(t, err)
		cold := newToolsetCatalog(store, clock)
		require.NoError(t, cold.validatePersistedEntries(t.Context()))
		loaded, err := cold.ActiveRegistration(t.Context(), toolset.Name)
		require.NoError(t, err)
		assert.Equal(t, state.SchemaFingerprint, loaded.SchemaFingerprint)
		assert.Equal(t, state.RegistrationToken, loaded.RegistrationToken)
		assert.Equal(t, raw, loaded.Toolset.raw)
		decoded, err := loaded.Toolset.decode("")
		require.NoError(t, err)
		assert.Equal(t, toolset, decoded)
	}
}

// savedFixtureFingerprint gives the one-tool fixture its explicit admission
// identity using supplied contract bytes, without calling the saved decoder.
func savedFixtureFingerprint(toolset *genregistry.Toolset, contract []byte) string {
	tool := toolset.Tools[0]
	var version *string
	if toolset.Version != nil {
		value := string(*toolset.Version)
		version = &value
	}
	return internaladmission.SchemaFingerprint(internaladmission.Schema{
		Name: toolset.Name, Description: toolset.Description, Version: version, Tags: toolset.Tags,
		Tools: []internaladmission.ToolSchema{{
			Name: tool.Name, Description: tool.Description, Tags: tool.Tags,
			PayloadSchema: tool.PayloadSchema, ExecutionPayloadSchema: tool.ExecutionPayloadSchema,
			ResultSchema: tool.ResultSchema, SidecarSchema: tool.SidecarSchema, ConsumerContract: contract,
		}},
	})
}

// A fresh registry must reload selected-branch metadata without changing the
// provider's accepted definition, fingerprint, or registration token.
func TestCatalogSavedUnionSelectionsKeepRegistration(t *testing.T) {
	for _, selection := range []genregistry.ToolUnionSelection{
		genregistry.NewToolUnionSelectionTagged(&genregistry.ToolTaggedUnionBranch{
			Discriminator: []*genregistry.ToolFieldPathSegment{{Segment: genregistry.NewToolFieldSegmentField("type")}},
			Value:         "lookup",
		}),
		genregistry.NewToolUnionSelectionUntagged(&genregistry.ToolUntaggedUnionBranch{JSONKind: "object", Index: 0}),
	} {
		t.Run(string(selection.Kind()), func(t *testing.T) {
			toolset := testDefinitionToolset()
			toolset.Tools[0].ConsumerContract.Payload.Fields[0].Branches = []*genregistry.ToolUnionBranch{{Selection: selection}}
			definition := testCatalogDefinition(t, toolset)
			clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
			store := newTestCatalogMap(clock)
			writer := newToolsetCatalog(store, clock)
			state, err := writer.Register(t.Context(), definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
			require.NoError(t, err)
			cold := newToolsetCatalog(store, clock)
			require.NoError(t, cold.validatePersistedEntries(t.Context()))
			loaded, err := cold.ActiveRegistration(t.Context(), toolset.Name)
			require.NoError(t, err)
			assert.Equal(t, state.RegistrationToken, loaded.RegistrationToken)
			assert.Equal(t, definition.fingerprint, loaded.SchemaFingerprint)
			assert.Equal(t, definition.raw, loaded.Toolset.raw)
			decoded, err := loaded.Toolset.decode("")
			require.NoError(t, err)
			assert.Equal(t, toolset, decoded)
		})
	}
}
