package dsl_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	. "goa.design/goa-ai/dsl"
	agentsexpr "goa.design/goa-ai/expr/agent"
	. "goa.design/goa/v3/dsl"
)

func TestNativeImageRequiresEvidenceAudience(t *testing.T) {
	for _, audience := range []string{"evidence", "timeline", "default"} {
		t.Run(audience, func(t *testing.T) {
			err := runDSLWithError(t, func() {
				API("images", func() {})
				Service("images", func() {
					Agent("reader", "Reads selected images.", func() {
						Use("images", func() {
							Tool("view", "View an image.", func() {
								ServerData("images.source.v1", String, func() {
									switch audience {
									case "evidence":
										AudienceEvidence()
									case "timeline":
										AudienceTimeline()
									}
									NativeImage()
								})
							})
						})
					})
				})
			})
			if audience != "evidence" {
				require.ErrorContains(t, err, "NativeImage requires AudienceEvidence")
				return
			}
			require.NoError(t, err)
			require.True(t, agentsexpr.Root.Agents[0].Used.Toolsets[0].Tools[0].ServerData[0].NativeImage)
		})
	}
}

func TestNativeImageRequiresServerDataScope(t *testing.T) {
	err := runDSLWithError(t, func() {
		API("images", func() { NativeImage() })
	})
	require.Error(t, err)
}
