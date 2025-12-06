package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFederationExpr_EvalName(t *testing.T) {
	f := &FederationExpr{}
	require.Equal(t, "federation configuration", f.EvalName())
}

func TestFederationExpr_IncludeExclude(t *testing.T) {
	t.Run("empty patterns", func(t *testing.T) {
		f := &FederationExpr{}
		require.Empty(t, f.Include)
		require.Empty(t, f.Exclude)
	})

	t.Run("with include patterns", func(t *testing.T) {
		f := &FederationExpr{
			Include: []string{"web-search", "code-execution"},
		}
		require.Equal(t, []string{"web-search", "code-execution"}, f.Include)
		require.Empty(t, f.Exclude)
	})

	t.Run("with exclude patterns", func(t *testing.T) {
		f := &FederationExpr{
			Exclude: []string{"experimental/*", "deprecated/*"},
		}
		require.Empty(t, f.Include)
		require.Equal(t, []string{"experimental/*", "deprecated/*"}, f.Exclude)
	})

	t.Run("with both include and exclude patterns", func(t *testing.T) {
		f := &FederationExpr{
			Include: []string{"web-search", "code-execution"},
			Exclude: []string{"experimental/*"},
		}
		require.Equal(t, []string{"web-search", "code-execution"}, f.Include)
		require.Equal(t, []string{"experimental/*"}, f.Exclude)
	})
}
