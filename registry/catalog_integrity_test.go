// Historical declarations keep their original identity even when generated
// consumer metadata has changed. Startup still rejects damaged saved pairs.
package registry

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/toolregistry"
)

func TestHistoricalCatalogStartupIntegrity(t *testing.T) {
	for _, retired := range []bool{false, true} {
		for _, contract := range []string{
			`{ "Kind":"service", "Bounds":{"Paging": {"ReplayPayload":true}}, "Title":"Read \u0072ecords" }`,
			`{"Kind":"service","Bounds":{"Paging":{"ReplayPayload":false}}}`,
			`null`,
		} {
			t.Run(fmt.Sprintf("%s/retired=%t", contract, retired), func(t *testing.T) {
				clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
				store := newTestCatalogMap(clock)
				state, definition := historicalCatalogFixture(t, contract, retired)
				key := toolsetCatalogKey(state.Info.Name)
				raw, err := marshalCatalogState(state)
				require.NoError(t, err)
				store.content[key], store.definitions[key] = raw, definition
				if retired {
					store.retiredTokens[state.RegistrationToken] = struct{}{}
				}
				catalog := newToolsetCatalog(store, clock)
				require.NoError(t, catalog.validatePersistedEntries(t.Context()))
				assert.Equal(t, raw, store.content[key])
				assert.Equal(t, definition, store.definitions[key])
				assert.Zero(t, store.definitionWrites)
				assert.Empty(t, catalog.definitions, "historical integrity must not cache executable validation")
				if contract != "null" {
					_, err = catalog.snapshot(t.Context(), state.Info.Name)
					require.ErrorContains(t, err, "ReplayPayload")
				}
			})
		}
	}
}

func TestHistoricalCatalogRejectsDamagedStorage(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*catalogState, *string, *testCatalogMap)
		want   string
	}{
		{"contract tamper", func(_ *catalogState, raw *string, _ *testCatalogMap) {
			*raw = strings.Replace(*raw, "true", "false", 1)
		}, "fingerprint"},
		{"summary tamper", func(s *catalogState, _ *string, _ *testCatalogMap) { s.Info.Description = new("changed") }, "metadata"},
		{"unknown envelope", func(_ *catalogState, raw *string, _ *testCatalogMap) {
			*raw = strings.Replace(*raw, `"RegisteredAt":""`, `"RegisteredAt":"","Unknown":true`, 1)
		}, "unknown field"},
		{"duplicate contract", func(_ *catalogState, raw *string, _ *testCatalogMap) {
			*raw = strings.Replace(*raw, `"ReplayPayload":true`, `"ReplayPayload":true,"ReplayPayload":true`, 1)
		}, "duplicate"},
		{"missing definition", func(s *catalogState, _ *string, m *testCatalogMap) {
			delete(m.definitions, toolsetCatalogKey(s.Info.Name))
		}, "INCOMPLETE"},
		{"missing state", func(s *catalogState, _ *string, m *testCatalogMap) { delete(m.content, toolsetCatalogKey(s.Info.Name)) }, "INCOMPLETE"},
		{"retirement mismatch", func(s *catalogState, _ *string, m *testCatalogMap) { m.retiredTokens[s.RegistrationToken] = struct{}{} }, "retirement"},
		{"current strict", func(s *catalogState, _ *string, _ *testCatalogMap) {
			s.WireProtocolVersion = toolregistry.WireProtocolVersion
			token, err := admissionRegistrationToken(s.SchemaFingerprint, s.AdmissionRevision, s.WireProtocolVersion)
			require.NoError(t, err)
			s.RegistrationToken = token
		}, "ReplayPayload"},
		{"native strict", func(s *catalogState, _ *string, _ *testCatalogMap) {
			s.NativeAgent = true
			s.WireProtocolVersion = 0
			s.AdmissionRevision = ""
			s.HealthEpoch = 0
			s.RegistrationToken = nativeAgentToken(s.SchemaFingerprint)
		}, "ReplayPayload"},
	} {
		t.Run(test.name, func(t *testing.T) {
			clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
			store := newTestCatalogMap(clock)
			state, definition := historicalCatalogFixture(t, `{"Bounds":{"Paging":{"ReplayPayload":true}}}`, false)
			key := toolsetCatalogKey(state.Info.Name)
			// Apply mutations before and after storage so absent-pair cases can
			// remove one side while field mutations retain their edited bytes.
			raw, err := marshalCatalogState(state)
			require.NoError(t, err)
			store.content[key], store.definitions[key] = raw, definition
			test.change(&state, &definition, store)
			if _, exists := store.content[key]; exists {
				store.content[key], err = marshalCatalogState(state)
				require.NoError(t, err)
			}
			if _, exists := store.definitions[key]; exists {
				store.definitions[key] = definition
			}
			err = newToolsetCatalog(store, clock).validatePersistedEntries(t.Context())
			require.ErrorContains(t, err, test.want)
		})
	}
}

// historicalCatalogFixture models an older writer with explicit original
// contract bytes and derives its token from that contract's fingerprint.
func historicalCatalogFixture(t *testing.T, contract string, retired bool) (catalogState, string) {
	t.Helper()
	toolset := testDefinitionToolset()
	definition := testCatalogDefinition(t, toolset)
	current, err := json.Marshal(toolset.Tools[0].ConsumerContract)
	require.NoError(t, err)
	definition.raw = strings.Replace(definition.raw, `"ConsumerContract":`+string(current), `"ConsumerContract":`+contract, 1)
	var original []byte
	if contract != "null" {
		original = []byte(contract)
	}
	definition.fingerprint = savedFixtureFingerprint(toolset, original)
	token, err := admissionRegistrationToken(definition.fingerprint, testAdmissionRevisionA, 13)
	require.NoError(t, err)
	state := newCatalogState(definition, testAdmissionRevisionA, token, time.Unix(1_700_000_000, 0))
	state.WireProtocolVersion = 13
	if retired {
		state.State = catalogEntryRetired
	}
	return state, definition.raw
}
