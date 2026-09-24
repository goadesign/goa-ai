//go:build integration

package registry

// A disconnected declaration remains visible to the running catalog scheduler,
// without causing provider traffic. Attachment still needs a current-epoch pong.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
)

const healthObservationSpan = "toolregistry.health"

func TestRedisDeclaredToolsetSchedulerObservesOfflineWithoutProviderTraffic(t *testing.T) {
	recorder := newHealthSpanRecorder(t)
	ctx := t.Context()
	rdb := getRedis(t)
	reg, err := New(ctx, Config{
		Redis: rdb, Name: t.Name(), PingInterval: 100 * time.Millisecond,
		ExpectedToolsets: []string{"inventory"},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reg.Close(context.Background())) })
	client, _ := startServiceAndClients(t, reg.Service())
	declared, err := client.DeclareServiceToolset(ctx, testServiceDeclaration())
	require.NoError(t, err)
	tracker := reg.healthTracker.(*healthTracker)
	require.Eventually(t, func() bool {
		for _, span := range recorder.Ended() {
			attrs := healthSpanAttributes(span)
			if span.Name() == healthObservationSpan &&
				attrs["toolregistry.registry"].AsString() == tracker.leaseScope &&
				attrs["toolregistry.toolset"].AsString() == "inventory" {
				return true
			}
		}
		return false
	}, time.Second, time.Millisecond)
	present, catalogSeen, offline := false, false, false
	for _, span := range recorder.Ended() {
		attrs := healthSpanAttributes(span)
		if attrs["toolregistry.registry"].AsString() != tracker.leaseScope {
			continue
		}
		switch span.Name() {
		case "toolregistry.catalog.expectation":
			present = present || attrs["toolregistry.present"].AsInt64() == 1
		case "toolregistry.catalog.entry":
			catalogSeen = true
		case healthObservationSpan:
			offline = true
			assert.Equal(t, codes.Ok, span.Status().Code)
			assert.Equal(t, attribute.INT64, attrs["toolregistry.provider_count"].Type())
			assert.Equal(t, attribute.INT64, attrs["toolregistry.ready"].Type())
			assert.Equal(t, attribute.BOOL, attrs["toolregistry.pong_seen"].Type())
			assert.Zero(t, attrs["toolregistry.provider_count"].AsInt64())
			assert.Zero(t, attrs["toolregistry.ready"].AsInt64())
			assert.False(t, attrs["toolregistry.pong_seen"].AsBool())
		}
	}
	assert.True(t, present)
	assert.True(t, catalogSeen)
	assert.True(t, offline)
	owner, err := rdb.Get(ctx, tracker.pingLeaseKey("inventory")).Result()
	require.NoError(t, err)
	assert.Equal(t, tracker.nodeID, owner)
	ttl, err := rdb.PTTL(ctx, tracker.pingLeaseKey("inventory")).Result()
	require.NoError(t, err)
	assert.Positive(t, ttl)
	assert.LessOrEqual(t, ttl, tracker.pingInterval)
	saved, err := reg.service.catalog.activeState(ctx, "inventory")
	require.NoError(t, err)
	assert.Empty(t, saved.ProviderLeases)
	assert.Positive(t, saved.HealthEpoch)
	assert.Zero(t, saved.LastPongUnixNano)
	manager := reg.streamManager.(*streamManager)
	manager.mu.RLock()
	handleCount := len(manager.streams)
	manager.mu.RUnlock()
	assert.Zero(t, handleCount, "declaration and offline sampling create no provider stream handle")
	keys, err := rdb.Keys(ctx, "*").Result()
	require.NoError(t, err)
	for _, key := range keys {
		kind, err := rdb.Type(ctx, key).Result()
		require.NoError(t, err)
		assert.NotEqual(t, "stream", kind, "the dedicated test database contains no provider traffic")
	}

	_, err = client.AttachProvider(ctx, &genregistry.AttachProviderPayload{
		Name: "inventory", ExpectedRegistrationToken: declared.RegistrationToken, ProviderID: "provider",
		ProviderIncarnationID: testIncarnationA, WireProtocolVersion: toolregistry.WireProtocolVersion,
	})
	require.NoError(t, err)
	check := &genregistry.CheckAdmissionPayload{Name: "inventory", ExpectedRegistrationToken: declared.RegistrationToken}
	ready, err := client.CheckAdmission(ctx, check)
	require.NoError(t, err)
	assert.False(t, ready.Ready)
	var ping toolregistry.ToolCallMessage
	require.Eventually(t, func() bool {
		events, err := rdb.XRangeN(ctx, pulseStreamKeyPrefix+toolregistry.ToolsetStreamID("inventory"), "-", "+", 1).Result()
		if err != nil {
			return false
		}
		for _, event := range events {
			payload, ok := event.Values["p"].(string)
			if ok && json.Unmarshal([]byte(payload), &ping) == nil && ping.Type == toolregistry.MessageTypePing {
				return true
			}
		}
		return false
	}, time.Second, time.Millisecond)
	token, epoch, valid := parsePingID(ping.PingID)
	require.True(t, valid)
	assert.Equal(t, declared.RegistrationToken, token)
	assert.Greater(t, epoch, saved.HealthEpoch)
	require.NoError(t, client.Pong(ctx, &genregistry.PongPayload{
		Toolset: "inventory", ProviderID: "provider", ProviderIncarnationID: testIncarnationA, PingID: ping.PingID,
	}))
	ready, err = client.CheckAdmission(ctx, check)
	require.NoError(t, err)
	assert.True(t, ready.Ready)
}

func TestRedisDeclaredToolsetVisibleWhenAnotherNodeOwnsSampling(t *testing.T) {
	recorder := newHealthSpanRecorder(t)
	ctx := t.Context()
	rdb := getRedis(t)
	reg, err := New(ctx, Config{
		Redis: rdb, Name: t.Name(), PingInterval: time.Hour, ExpectedToolsets: []string{"inventory"},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reg.Close(context.Background())) })
	client, _ := startServiceAndClients(t, reg.Service())
	tracker := reg.healthTracker.(*healthTracker)
	require.NoError(t, rdb.Set(ctx, tracker.pingLeaseKey("inventory"), "other-node", tracker.pingInterval).Err())
	_, err = client.DeclareServiceToolset(ctx, testServiceDeclaration())
	require.NoError(t, err)
	tracker.runHealthSweep(ctx)
	present, catalogSeen := false, false
	for _, span := range recorder.Ended() {
		attrs := healthSpanAttributes(span)
		if attrs["toolregistry.registry"].AsString() != tracker.leaseScope {
			continue
		}
		switch span.Name() {
		case "toolregistry.catalog.expectation":
			present = attrs["toolregistry.present"].AsInt64() == 1
		case "toolregistry.catalog.entry":
			catalogSeen = true
		case healthObservationSpan:
			t.Error("the node that lost sampling must not invent a health observation")
		}
	}
	assert.True(t, present)
	assert.True(t, catalogSeen)
	owner, err := rdb.Get(ctx, tracker.pingLeaseKey("inventory")).Result()
	require.NoError(t, err)
	assert.Equal(t, "other-node", owner)
}
